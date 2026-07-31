package stock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// Alerter is notified when stock crosses a threshold. The notification module
// implements it.
type Alerter interface {
	LowStock(ctx context.Context, orgID, productID, warehouseID uuid.UUID, onHand, minStock money.Decimal)
}

// Sequencer allocates human-readable document codes (TRF-2026-000042).
type Sequencer interface {
	Next(ctx context.Context, tx *gorm.DB, orgID uuid.UUID, docType, prefix string) (string, error)
}

// Service holds stock business logic.
type Service struct {
	db      *gorm.DB
	ledger  *Ledger
	audit   *shared.Auditor
	alerter Alerter
	seq     Sequencer
	lg      zerolog.Logger
}

// NewService builds the stock service.
func NewService(
	db *gorm.DB,
	ledger *Ledger,
	audit *shared.Auditor,
	alerter Alerter,
	seq Sequencer,
	lg zerolog.Logger,
) *Service {
	return &Service{
		db: db, ledger: ledger, audit: audit, alerter: alerter, seq: seq,
		lg: lg.With().Str("module", "stock").Logger(),
	}
}

// AdjustInput is a manual stock change: a receipt without a purchase order, a
// write-off, or a correction. It is also the payload the mobile scanner posts.
type AdjustInput struct {
	ProductID   uuid.UUID  `json:"product_id"   validate:"required"`
	VariantID   *uuid.UUID `json:"variant_id"`
	WarehouseID uuid.UUID  `json:"warehouse_id" validate:"required"`
	LocationID  *uuid.UUID `json:"location_id"`
	BatchID     *uuid.UUID `json:"batch_id"`

	// Quantity is signed: positive adds, negative removes.
	Quantity money.Decimal  `json:"quantity"`
	UnitCost *money.Decimal `json:"unit_cost"`
	Reason   string         `json:"reason" validate:"required,min=2,max=200"`
	Note     string         `json:"note"   validate:"omitempty,max=1000"`

	// ClientRequestID makes a retried offline scan idempotent.
	ClientRequestID string     `json:"client_request_id" validate:"omitempty,max=64"`
	OccurredAt      *time.Time `json:"occurred_at"`
}

// Adjust applies a manual stock movement.
func (s *Service) Adjust(ctx context.Context, actor auth.Identity, in AdjustInput, meta shared.Actor) (*MovementResult, error) {
	if money.IsZero(in.Quantity) {
		return nil, shared.Validation("quantity must not be zero")
	}
	if err := s.assertWarehouse(ctx, actor.OrgID, in.WarehouseID); err != nil {
		return nil, err
	}

	movementType := MovementAdjustment
	req := MovementRequest{
		OrgID:           actor.OrgID,
		Type:            movementType,
		ProductID:       in.ProductID,
		VariantID:       in.VariantID,
		WarehouseID:     in.WarehouseID,
		LocationID:      in.LocationID,
		BatchID:         in.BatchID,
		Delta:           money.Round(in.Quantity),
		UnitCost:        in.UnitCost,
		Reason:          in.Reason,
		Note:            in.Note,
		ReferenceType:   "manual_adjustment",
		ActorID:         actor.UserID,
		ClientRequestID: in.ClientRequestID,
	}
	if in.OccurredAt != nil {
		req.OccurredAt = in.OccurredAt.UTC()
	}

	var result *MovementResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if money.IsNegative(req.Delta) && in.LocationID == nil {
			// Removing stock without naming a bin: draw from wherever it sits.
			// Using Apply here would look for a warehouse-level row and report
			// "insufficient stock" while the goods are on a shelf — which is the
			// normal case after a goods receipt into a location.
			var results []*MovementResult
			results, _, err = s.ledger.Issue(ctx, tx, req)
			if err != nil {
				return err
			}
			// The first line carries the idempotency key and the audit before/after.
			result = results[0]
		} else {
			result, err = s.ledger.Apply(ctx, tx, req)
		}
		if err != nil {
			return err
		}
		if result.Duplicate {
			return nil
		}
		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "stock.adjust", EntityType: "stock_item", EntityID: &result.Item.ID,
			Before: map[string]any{"quantity": result.Movement.QuantityBefore},
			After: map[string]any{
				"quantity": result.Movement.QuantityAfter,
				"reason":   in.Reason,
			},
		})
	})
	if err != nil {
		return nil, err
	}

	if !result.Duplicate {
		s.checkLowStock(ctx, actor.OrgID, in.ProductID, in.WarehouseID)
	}
	return result, nil
}

// SyncRequest is a batch of offline-queued movements from the mobile app.
type SyncRequest struct {
	Movements []AdjustInput `json:"movements" validate:"required,min=1,max=200,dive"`
}

// SyncOutcome reports the fate of one queued movement.
type SyncOutcome struct {
	ClientRequestID string        `json:"client_request_id"`
	Status          string        `json:"status" example:"applied"`
	MovementID      *uuid.UUID    `json:"movement_id,omitempty"`
	Error           *shared.Error `json:"error,omitempty"`
}

// SyncResponse summarises a batch sync.
type SyncResponse struct {
	Applied   int           `json:"applied"`
	Duplicate int           `json:"duplicate"`
	Failed    int           `json:"failed"`
	Outcomes  []SyncOutcome `json:"outcomes"`
}

// Sync applies a batch of offline movements.
//
// Each movement is applied in its own transaction so one bad row (a deleted
// product, insufficient stock) does not discard the rest of the queue. The
// per-item outcome tells the device exactly which entries to clear locally and
// which to surface for human resolution — the conflict-resolution contract
// documented in ims-mobile/README.md.
func (s *Service) Sync(ctx context.Context, actor auth.Identity, in SyncRequest, meta shared.Actor) (*SyncResponse, error) {
	resp := &SyncResponse{Outcomes: make([]SyncOutcome, 0, len(in.Movements))}

	for _, mv := range in.Movements {
		outcome := SyncOutcome{ClientRequestID: mv.ClientRequestID}

		if mv.ClientRequestID == "" {
			// Without an idempotency key a retry would double-apply, so the
			// server refuses rather than risking it.
			outcome.Status = "failed"
			outcome.Error = shared.Validation("client_request_id is required for offline sync")
			resp.Failed++
			resp.Outcomes = append(resp.Outcomes, outcome)
			continue
		}

		result, err := s.Adjust(ctx, actor, mv, meta)
		switch {
		case err != nil:
			appErr := shared.AsError(err)
			if appErr == nil {
				appErr = shared.Internal("could not apply movement")
				s.lg.Error().Err(err).Str("client_request_id", mv.ClientRequestID).
					Msg("offline sync item failed")
			}
			outcome.Status = "failed"
			outcome.Error = appErr
			resp.Failed++
		case result.Duplicate:
			outcome.Status = "duplicate"
			outcome.MovementID = &result.Movement.ID
			resp.Duplicate++
		default:
			outcome.Status = "applied"
			outcome.MovementID = &result.Movement.ID
			resp.Applied++
		}
		resp.Outcomes = append(resp.Outcomes, outcome)
	}

	return resp, nil
}

// ListItems returns a page of on-hand stock rows.
func (s *Service) ListItems(ctx context.Context, actor auth.Identity, q shared.Query) ([]ItemView, shared.PageMeta, error) {
	base := s.db.WithContext(ctx).Table("stock_items si").
		Joins("JOIN products p ON p.id = si.product_id AND p.deleted_at IS NULL").
		Joins("JOIN warehouses w ON w.id = si.warehouse_id AND w.deleted_at IS NULL").
		Joins("LEFT JOIN locations l ON l.id = si.location_id").
		Joins("LEFT JOIN batches b ON b.id = si.batch_id").
		Where("si.organization_id = ?", actor.OrgID)

	if v := q.Filters["warehouse_id"]; v != "" {
		base = base.Where("si.warehouse_id = ?", v)
	}
	if v := q.Filters["product_id"]; v != "" {
		base = base.Where("si.product_id = ?", v)
	}
	if v := q.Filters["location_id"]; v != "" {
		base = base.Where("si.location_id = ?", v)
	}
	if q.Filters["nonzero"] == "true" {
		base = base.Where("si.quantity <> 0")
	}
	if q.Filters["below_min"] == "true" {
		base = base.Where("p.min_stock > 0 AND si.quantity <= p.min_stock")
	}
	if q.Search != "" {
		like := "%" + strings.ToLower(q.Search) + "%"
		base = base.Where("lower(p.name) LIKE ? OR lower(p.sku) LIKE ? OR p.barcode = ?", like, like, q.Search)
	}

	var total int64
	if err := base.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}

	sortable := map[string]string{
		"quantity": "si.quantity", "product_name": "p.name",
		"sku": "p.sku", "updated_at": "si.updated_at",
	}

	var rows []ItemView
	err := base.
		Select(`si.id, si.product_id, si.variant_id, si.warehouse_id, si.location_id, si.batch_id,
		        si.quantity, si.reserved_quantity, (si.quantity - si.reserved_quantity) AS available_quantity,
		        si.updated_at,
		        p.sku AS product_sku, p.name AS product_name, p.min_stock, p.max_stock,
		        w.code AS warehouse_code, w.name AS warehouse_name,
		        l.code AS location_code, b.lot_number, b.expiry_date`).
		Scopes(q.OrderBy(sortable, "p.name ASC"), q.Paginate()).
		Scan(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// ItemView is a denormalised stock row for list screens, carrying the product
// and warehouse names the UI needs without an N+1 of follow-up reads.
type ItemView struct {
	ID                uuid.UUID      `json:"id"`
	ProductID         uuid.UUID      `json:"product_id"`
	VariantID         *uuid.UUID     `json:"variant_id,omitempty"`
	WarehouseID       uuid.UUID      `json:"warehouse_id"`
	LocationID        *uuid.UUID     `json:"location_id,omitempty"`
	BatchID           *uuid.UUID     `json:"batch_id,omitempty"`
	Quantity          money.Decimal  `json:"quantity"`
	ReservedQuantity  money.Decimal  `json:"reserved_quantity"`
	AvailableQuantity money.Decimal  `json:"available_quantity"`
	UpdatedAt         time.Time      `json:"updated_at"`
	ProductSKU        string         `json:"product_sku"`
	ProductName       string         `json:"product_name"`
	MinStock          money.Decimal  `json:"min_stock"`
	MaxStock          *money.Decimal `json:"max_stock,omitempty"`
	WarehouseCode     string         `json:"warehouse_code"`
	WarehouseName     string         `json:"warehouse_name"`
	LocationCode      *string        `json:"location_code,omitempty"`
	LotNumber         *string        `json:"lot_number,omitempty"`
	ExpiryDate        *time.Time     `json:"expiry_date,omitempty"`
}

// ListMovements returns the audit ledger for stock.
func (s *Service) ListMovements(ctx context.Context, actor auth.Identity, q shared.Query) ([]Movement, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Movement{}).Where("organization_id = ?", actor.OrgID)

	for name, col := range map[string]string{
		"product_id": "product_id", "warehouse_id": "warehouse_id",
		"type": "type", "actor_id": "actor_id", "batch_id": "batch_id",
	} {
		if v := q.Filters[name]; v != "" {
			tx = tx.Where(col+" = ?", v)
		}
	}
	if v := q.Filters["from"]; v != "" {
		tx = tx.Where("occurred_at >= ?", v)
	}
	if v := q.Filters["to"]; v != "" {
		tx = tx.Where("occurred_at <= ?", v)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}

	sortable := map[string]string{"occurred_at": "occurred_at", "created_at": "created_at"}
	var rows []Movement
	err := tx.Scopes(q.OrderBy(sortable, "occurred_at DESC"), q.Paginate()).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- batches ---------------------------------------------------------------

// BatchInput creates a batch/lot.
type BatchInput struct {
	ProductID      uuid.UUID  `json:"product_id"      validate:"required"`
	LotNumber      string     `json:"lot_number"      validate:"required,min=1,max=64"`
	ExpiryDate     *time.Time `json:"expiry_date"`
	ManufacturedAt *time.Time `json:"manufactured_at"`
	SupplierID     *uuid.UUID `json:"supplier_id"`
}

// CreateBatch registers a batch.
func (s *Service) CreateBatch(ctx context.Context, actor auth.Identity, in BatchInput, meta shared.Actor) (*Batch, error) {
	b := Batch{
		ProductID: in.ProductID, LotNumber: strings.TrimSpace(in.LotNumber),
		ExpiryDate: in.ExpiryDate, ManufacturedAt: in.ManufacturedAt,
		SupplierID: in.SupplierID, ReceivedAt: time.Now().UTC(),
	}
	b.OrganizationID = actor.OrgID

	if err := s.db.WithContext(ctx).Create(&b).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("this lot number already exists for the product")
		}
		return nil, fmt.Errorf("create batch: %w", err)
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "batch.create", EntityType: "batch", EntityID: &b.ID, After: b,
	})
	return &b, nil
}

// ListBatches returns batches, optionally only those expiring within N days.
func (s *Service) ListBatches(ctx context.Context, actor auth.Identity, q shared.Query) ([]Batch, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Batch{}).Scopes(shared.InOrg(actor.OrgID))

	if v := q.Filters["product_id"]; v != "" {
		tx = tx.Where("product_id = ?", v)
	}
	if v := q.Filters["expiring_within_days"]; v != "" {
		tx = tx.Where("expiry_date IS NOT NULL AND expiry_date <= current_date + (? || ' days')::interval", v)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	sortable := map[string]string{"expiry_date": "expiry_date", "lot_number": "lot_number", "created_at": "created_at"}
	var rows []Batch
	err := tx.Scopes(q.OrderBy(sortable, "expiry_date ASC NULLS LAST"), q.Paginate()).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- transfers -------------------------------------------------------------

// TransferInput opens a transfer between two warehouses.
type TransferInput struct {
	FromWarehouseID uuid.UUID           `json:"from_warehouse_id" validate:"required"`
	ToWarehouseID   uuid.UUID           `json:"to_warehouse_id"   validate:"required"`
	Note            string              `json:"note"              validate:"omitempty,max=1000"`
	Lines           []TransferLineInput `json:"lines"             validate:"required,min=1,dive"`
}

// TransferLineInput is one product to move.
type TransferLineInput struct {
	ProductID uuid.UUID     `json:"product_id" validate:"required"`
	VariantID *uuid.UUID    `json:"variant_id"`
	BatchID   *uuid.UUID    `json:"batch_id"`
	Quantity  money.Decimal `json:"quantity"`
}

// CreateTransfer drafts a transfer. No stock moves until it is dispatched.
func (s *Service) CreateTransfer(ctx context.Context, actor auth.Identity, in TransferInput, meta shared.Actor) (*Transfer, error) {
	if in.FromWarehouseID == in.ToWarehouseID {
		return nil, shared.Validation("source and destination warehouses must differ")
	}
	for _, whID := range []uuid.UUID{in.FromWarehouseID, in.ToWarehouseID} {
		if err := s.assertWarehouse(ctx, actor.OrgID, whID); err != nil {
			return nil, err
		}
	}
	for _, line := range in.Lines {
		if !money.IsPositive(line.Quantity) {
			return nil, shared.Validation("every transfer line needs a positive quantity")
		}
	}

	t := Transfer{
		FromWarehouseID: in.FromWarehouseID,
		ToWarehouseID:   in.ToWarehouseID,
		Status:          TransferDraft,
	}
	t.OrganizationID = actor.OrgID
	if in.Note != "" {
		t.Note = &in.Note
	}
	if actor.UserID != uuid.Nil {
		id := actor.UserID
		t.CreatedBy = &id
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "stock_transfer", "TRF")
		if err != nil {
			return err
		}
		t.Code = code
		if err := tx.Create(&t).Error; err != nil {
			return fmt.Errorf("create transfer: %w", err)
		}

		lines := make([]TransferLine, 0, len(in.Lines))
		for _, l := range in.Lines {
			lines = append(lines, TransferLine{
				ID: uuid.New(), TransferID: t.ID,
				ProductID: l.ProductID, VariantID: l.VariantID, BatchID: l.BatchID,
				Quantity: money.Round(l.Quantity), ReceivedQuantity: money.Zero(),
			})
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create transfer lines: %w", err)
		}
		t.Lines = lines

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "transfer.create", EntityType: "stock_transfer", EntityID: &t.ID, After: t,
		})
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DispatchTransfer removes stock from the source warehouse and marks the
// transfer in transit. Between dispatch and receipt the goods belong to neither
// warehouse's on-hand figure, which is the honest representation of stock on a
// truck.
func (s *Service) DispatchTransfer(ctx context.Context, actor auth.Identity, transferID uuid.UUID, meta shared.Actor) (*Transfer, error) {
	var t Transfer
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", transferID).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("transfer")
			}
			return err
		}
		if t.Status != TransferDraft {
			return shared.InvalidTransition(string(t.Status), string(TransferInTransit))
		}

		for _, line := range t.Lines {
			if _, err := s.ledger.Apply(ctx, tx, MovementRequest{
				OrgID: actor.OrgID, Type: MovementTransferOut,
				ProductID: line.ProductID, VariantID: line.VariantID,
				WarehouseID: t.FromWarehouseID, BatchID: line.BatchID,
				Delta:         line.Quantity.Neg(),
				Reason:        "transfer " + t.Code,
				ReferenceType: "stock_transfer", ReferenceID: &t.ID,
				ActorID: actor.UserID,
			}); err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		actorID := actor.UserID
		if err := tx.Model(&Transfer{}).Where("id = ?", t.ID).Updates(map[string]any{
			"status": TransferInTransit, "dispatched_at": now, "dispatched_by": actorID,
		}).Error; err != nil {
			return err
		}
		t.Status, t.DispatchedAt, t.DispatchedBy = TransferInTransit, &now, &actorID

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "transfer.dispatch", EntityType: "stock_transfer", EntityID: &t.ID,
		})
	})
	if err != nil {
		return nil, err
	}

	for _, line := range t.Lines {
		s.checkLowStock(ctx, actor.OrgID, line.ProductID, t.FromWarehouseID)
	}
	return &t, nil
}

// ReceiveTransfer adds the dispatched stock to the destination warehouse.
func (s *Service) ReceiveTransfer(ctx context.Context, actor auth.Identity, transferID uuid.UUID, meta shared.Actor) (*Transfer, error) {
	var t Transfer
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", transferID).First(&t).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("transfer")
			}
			return err
		}
		if t.Status != TransferInTransit {
			return shared.InvalidTransition(string(t.Status), string(TransferReceived))
		}

		for _, line := range t.Lines {
			if _, err := s.ledger.Apply(ctx, tx, MovementRequest{
				OrgID: actor.OrgID, Type: MovementTransferIn,
				ProductID: line.ProductID, VariantID: line.VariantID,
				WarehouseID: t.ToWarehouseID, BatchID: line.BatchID,
				Delta:         line.Quantity,
				Reason:        "transfer " + t.Code,
				ReferenceType: "stock_transfer", ReferenceID: &t.ID,
				ActorID: actor.UserID,
			}); err != nil {
				return err
			}
			if err := tx.Model(&TransferLine{}).Where("id = ?", line.ID).
				Update("received_quantity", line.Quantity).Error; err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		actorID := actor.UserID
		if err := tx.Model(&Transfer{}).Where("id = ?", t.ID).Updates(map[string]any{
			"status": TransferReceived, "received_at": now, "received_by": actorID,
		}).Error; err != nil {
			return err
		}
		t.Status, t.ReceivedAt, t.ReceivedBy = TransferReceived, &now, &actorID

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "transfer.receive", EntityType: "stock_transfer", EntityID: &t.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CancelTransfer voids a draft transfer. An in-transit transfer cannot be
// cancelled: the stock has physically left, so it must be received (or
// received short and adjusted).
func (s *Service) CancelTransfer(ctx context.Context, actor auth.Identity, transferID uuid.UUID, meta shared.Actor) error {
	var t Transfer
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("id = ?", transferID).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return shared.NotFound("transfer")
	}
	if err != nil {
		return err
	}
	if t.Status != TransferDraft {
		return shared.InvalidTransition(string(t.Status), string(TransferCancelled))
	}
	if err := s.db.WithContext(ctx).Model(&Transfer{}).Where("id = ?", t.ID).
		Update("status", TransferCancelled).Error; err != nil {
		return err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "transfer.cancel", EntityType: "stock_transfer", EntityID: &t.ID,
	})
	return nil
}

// GetTransfer loads a transfer with its lines.
func (s *Service) GetTransfer(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Transfer, error) {
	var t Transfer
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Where("id = ?", id).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("transfer")
	}
	return &t, err
}

// ListTransfers returns a page of transfers.
func (s *Service) ListTransfers(ctx context.Context, actor auth.Identity, q shared.Query) ([]Transfer, shared.PageMeta, error) {
	filterable := map[string]string{
		"status": "status", "from_warehouse_id": "from_warehouse_id", "to_warehouse_id": "to_warehouse_id",
	}
	tx := s.db.WithContext(ctx).Model(&Transfer{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	sortable := map[string]string{"created_at": "created_at", "code": "code", "status": "status"}
	var rows []Transfer
	err := tx.Scopes(q.OrderBy(sortable, "created_at DESC"), q.Paginate()).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- cycle counts ----------------------------------------------------------

// CycleCountInput opens a stock-take over a warehouse or one of its locations.
type CycleCountInput struct {
	WarehouseID uuid.UUID  `json:"warehouse_id" validate:"required"`
	LocationID  *uuid.UUID `json:"location_id"`
	Note        string     `json:"note"         validate:"omitempty,max=1000"`
	// ProductIDs narrows the count; empty means every product holding stock at
	// the chosen scope.
	ProductIDs []uuid.UUID `json:"product_ids"`
}

// CreateCycleCount snapshots current quantities into a count sheet.
func (s *Service) CreateCycleCount(ctx context.Context, actor auth.Identity, in CycleCountInput, meta shared.Actor) (*CycleCount, error) {
	if err := s.assertWarehouse(ctx, actor.OrgID, in.WarehouseID); err != nil {
		return nil, err
	}

	cc := CycleCount{WarehouseID: in.WarehouseID, LocationID: in.LocationID, Status: CountCounting}
	cc.OrganizationID = actor.OrgID
	if in.Note != "" {
		cc.Note = &in.Note
	}
	if actor.UserID != uuid.Nil {
		id := actor.UserID
		cc.CreatedBy = &id
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "cycle_count", "CC")
		if err != nil {
			return err
		}
		cc.Code = code
		if err := tx.Create(&cc).Error; err != nil {
			return err
		}

		itemQuery := tx.Model(&Item{}).
			Where("organization_id = ? AND warehouse_id = ?", actor.OrgID, in.WarehouseID)
		if in.LocationID != nil {
			itemQuery = itemQuery.Where("location_id = ?", *in.LocationID)
		}
		if len(in.ProductIDs) > 0 {
			itemQuery = itemQuery.Where("product_id IN ?", in.ProductIDs)
		}

		var items []Item
		if err := itemQuery.Find(&items).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return shared.Validation("no stock found at this scope to count")
		}

		lines := make([]CycleCountLine, 0, len(items))
		for _, it := range items {
			lines = append(lines, CycleCountLine{
				ID: uuid.New(), CycleCountID: cc.ID,
				ProductID: it.ProductID, VariantID: it.VariantID,
				BatchID: it.BatchID, LocationID: it.LocationID,
				ExpectedQuantity: it.Quantity,
			})
		}
		if err := tx.Create(&lines).Error; err != nil {
			return err
		}
		cc.Lines = lines

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "cycle_count.create", EntityType: "cycle_count", EntityID: &cc.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &cc, nil
}

// RecordCountInput submits counted quantities.
type RecordCountInput struct {
	Lines []RecordCountLine `json:"lines" validate:"required,min=1,dive"`
}

// RecordCountLine is one counted line.
type RecordCountLine struct {
	LineID  uuid.UUID     `json:"line_id"  validate:"required"`
	Counted money.Decimal `json:"counted_quantity"`
	Note    string        `json:"note"     validate:"omitempty,max=500"`
}

// RecordCount saves counted quantities without yet adjusting stock, so a
// supervisor can review variances before they hit the ledger.
func (s *Service) RecordCount(ctx context.Context, actor auth.Identity, countID uuid.UUID, in RecordCountInput) (*CycleCount, error) {
	cc, err := s.GetCycleCount(ctx, actor, countID)
	if err != nil {
		return nil, err
	}
	if cc.Status != CountCounting && cc.Status != CountReview {
		return nil, shared.InvalidTransition(string(cc.Status), string(CountReview))
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, l := range in.Lines {
			if money.IsNegative(l.Counted) {
				return shared.Validation("counted quantity cannot be negative")
			}
			updates := map[string]any{
				"counted_quantity": money.Round(l.Counted),
				"counted_at":       now,
				"counted_by":       actor.UserID,
			}
			if l.Note != "" {
				updates["note"] = l.Note
			}
			res := tx.Model(&CycleCountLine{}).
				Where("id = ? AND cycle_count_id = ?", l.LineID, countID).
				Updates(updates)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return shared.NotFound("cycle count line " + l.LineID.String())
			}
		}
		return tx.Model(&CycleCount{}).Where("id = ?", countID).
			Update("status", CountReview).Error
	})
	if err != nil {
		return nil, err
	}
	return s.GetCycleCount(ctx, actor, countID)
}

// CompleteCycleCount posts every variance to the ledger as a `count` movement
// and closes the count.
func (s *Service) CompleteCycleCount(ctx context.Context, actor auth.Identity, countID uuid.UUID, meta shared.Actor) (*CycleCount, error) {
	var cc CycleCount
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", countID).First(&cc).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("cycle count")
			}
			return err
		}
		if cc.Status != CountReview && cc.Status != CountCounting {
			return shared.InvalidTransition(string(cc.Status), string(CountCompleted))
		}

		uncounted := 0
		for _, line := range cc.Lines {
			if line.CountedQuantity == nil {
				uncounted++
				continue
			}
			variance := line.Variance()
			if money.IsZero(variance) {
				continue
			}
			if _, err := s.ledger.Apply(ctx, tx, MovementRequest{
				OrgID: actor.OrgID, Type: MovementCount,
				ProductID: line.ProductID, VariantID: line.VariantID,
				WarehouseID: cc.WarehouseID, LocationID: line.LocationID, BatchID: line.BatchID,
				Delta:         variance,
				Reason:        "cycle count " + cc.Code,
				ReferenceType: "cycle_count", ReferenceID: &cc.ID,
				ActorID: actor.UserID,
			}); err != nil {
				return err
			}
		}
		if uncounted > 0 {
			return shared.Validation(fmt.Sprintf("%d line(s) have not been counted yet", uncounted))
		}

		now := time.Now().UTC()
		actorID := actor.UserID
		if err := tx.Model(&CycleCount{}).Where("id = ?", cc.ID).Updates(map[string]any{
			"status": CountCompleted, "completed_at": now, "completed_by": actorID,
		}).Error; err != nil {
			return err
		}
		cc.Status, cc.CompletedAt, cc.CompletedBy = CountCompleted, &now, &actorID

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "cycle_count.complete", EntityType: "cycle_count", EntityID: &cc.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &cc, nil
}

// GetCycleCount loads a count with its lines.
func (s *Service) GetCycleCount(ctx context.Context, actor auth.Identity, id uuid.UUID) (*CycleCount, error) {
	var cc CycleCount
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Where("id = ?", id).First(&cc).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("cycle count")
	}
	return &cc, err
}

// ListCycleCounts returns a page of counts.
func (s *Service) ListCycleCounts(ctx context.Context, actor auth.Identity, q shared.Query) ([]CycleCount, shared.PageMeta, error) {
	filterable := map[string]string{"status": "status", "warehouse_id": "warehouse_id"}
	tx := s.db.WithContext(ctx).Model(&CycleCount{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []CycleCount
	err := tx.Scopes(
		q.OrderBy(map[string]string{"created_at": "created_at", "code": "code"}, "created_at DESC"),
		q.Paginate(),
	).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- internals -------------------------------------------------------------

// ApplyInTx exposes the ledger to sibling modules (purchasing receipts, sales
// dispatch) so they can move stock inside their own transaction.
func (s *Service) ApplyInTx(ctx context.Context, tx *gorm.DB, req MovementRequest) (*MovementResult, error) {
	return s.ledger.Apply(ctx, tx, req)
}

// IssueInTx removes stock from a warehouse without naming a bin, drawing from
// whichever locations and batches hold it. Outbound callers (sales dispatch,
// transfer dispatch) use this rather than ApplyInTx, because they know the
// warehouse but not the shelf.
func (s *Service) IssueInTx(ctx context.Context, tx *gorm.DB, req MovementRequest) ([]*MovementResult, money.Decimal, error) {
	return s.ledger.Issue(ctx, tx, req)
}

// Reserve allocates on-hand stock to a confirmed order without moving it.
func (s *Service) Reserve(ctx context.Context, tx *gorm.DB, orgID, productID uuid.UUID, variantID *uuid.UUID, warehouseID uuid.UUID, qty money.Decimal) error {
	var items []Item
	q := tx.WithContext(ctx).
		Where("organization_id = ? AND product_id = ? AND warehouse_id = ? AND quantity > reserved_quantity",
			orgID, productID, warehouseID).
		Order("created_at ASC")
	if variantID != nil {
		q = q.Where("variant_id = ?", *variantID)
	}
	if err := q.Find(&items).Error; err != nil {
		return err
	}

	remaining := qty
	for i := range items {
		if !money.IsPositive(remaining) {
			break
		}
		it := items[i]
		free := it.Available()
		take := money.Min(free, remaining)
		if !money.IsPositive(take) {
			continue
		}
		if err := tx.WithContext(ctx).Model(&Item{}).Where("id = ?", it.ID).
			Update("reserved_quantity", it.ReservedQuantity.Add(take)).Error; err != nil {
			return err
		}
		remaining = remaining.Sub(take)
	}
	if money.IsPositive(remaining) {
		return shared.InsufficientStock(
			qty.Sub(remaining).InexactFloat64(), qty.InexactFloat64())
	}
	return nil
}

// Release returns reserved stock to the available pool, used when an order is
// cancelled or after its stock has actually shipped.
func (s *Service) Release(ctx context.Context, tx *gorm.DB, orgID, productID uuid.UUID, variantID *uuid.UUID, warehouseID uuid.UUID, qty money.Decimal) error {
	var items []Item
	q := tx.WithContext(ctx).
		Where("organization_id = ? AND product_id = ? AND warehouse_id = ? AND reserved_quantity > 0",
			orgID, productID, warehouseID).
		Order("created_at ASC")
	if variantID != nil {
		q = q.Where("variant_id = ?", *variantID)
	}
	if err := q.Find(&items).Error; err != nil {
		return err
	}

	remaining := qty
	for i := range items {
		if !money.IsPositive(remaining) {
			break
		}
		it := items[i]
		take := money.Min(it.ReservedQuantity, remaining)
		if err := tx.WithContext(ctx).Model(&Item{}).Where("id = ?", it.ID).
			Update("reserved_quantity", it.ReservedQuantity.Sub(take)).Error; err != nil {
			return err
		}
		remaining = remaining.Sub(take)
	}
	return nil
}

func (s *Service) assertWarehouse(ctx context.Context, orgID, warehouseID uuid.UUID) error {
	var count int64
	err := s.db.WithContext(ctx).Table("warehouses").
		Where("id = ? AND organization_id = ? AND deleted_at IS NULL AND is_active",
			warehouseID, orgID).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return shared.Validation("warehouse does not exist or is inactive")
	}
	return nil
}

// checkLowStock compares total on-hand against the product's minimum and fires
// an alert when it has fallen to or below it. Failures here never fail the
// stock operation that triggered them.
func (s *Service) checkLowStock(ctx context.Context, orgID, productID, warehouseID uuid.UUID) {
	var row struct {
		OnHand   money.Decimal
		MinStock money.Decimal
	}
	err := s.db.WithContext(ctx).Table("products p").
		Select(`p.min_stock AS min_stock,
		        COALESCE((SELECT SUM(si.quantity) FROM stock_items si
		                  WHERE si.product_id = p.id AND si.warehouse_id = ?), 0) AS on_hand`,
			warehouseID).
		Where("p.id = ? AND p.organization_id = ?", productID, orgID).
		Scan(&row).Error
	if err != nil {
		s.lg.Error().Err(err).Msg("low stock check failed")
		return
	}
	if money.IsPositive(row.MinStock) && row.OnHand.LessThanOrEqual(row.MinStock) {
		s.alerter.LowStock(ctx, orgID, productID, warehouseID, row.OnHand, row.MinStock)
	}
}
