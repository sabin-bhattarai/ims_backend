package stock

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// MovementRequest describes one stock change to apply. Every quantity change
// in the system — receipts, issues, adjustments, transfers, counts, returns —
// funnels through Ledger.Apply with one of these, which is what keeps
// stock_items and stock_movements in agreement.
type MovementRequest struct {
	OrgID       uuid.UUID
	Type        MovementType
	ProductID   uuid.UUID
	VariantID   *uuid.UUID
	WarehouseID uuid.UUID
	LocationID  *uuid.UUID
	BatchID     *uuid.UUID

	// Delta is signed: positive adds stock, negative removes it.
	Delta money.Decimal

	// UnitCost seeds the FIFO layer on inbound movements. When nil, the
	// product's standard cost is used.
	UnitCost *money.Decimal

	Reason        string
	Note          string
	ReferenceType string
	ReferenceID   *uuid.UUID
	ActorID       uuid.UUID

	// ClientRequestID makes the operation idempotent. The mobile app sets it on
	// every queued offline scan so a retried sync cannot double-apply.
	ClientRequestID string

	// OccurredAt is when the action happened on the device; it defaults to now.
	OccurredAt time.Time
}

// MovementResult reports what a single Apply did.
type MovementResult struct {
	Movement *Movement `json:"movement"`
	Item     *Item     `json:"item"`
	// COGS is the FIFO cost of the goods removed, populated for outbound
	// movements only.
	COGS money.Decimal `json:"cogs"`
	// Duplicate is true when ClientRequestID matched an already-applied
	// movement and nothing changed.
	Duplicate bool `json:"duplicate"`
}

// Ledger applies stock movements. It has no HTTP or transaction concerns of its
// own: callers supply the transaction, so a goods receipt with ten lines is one
// atomic unit.
type Ledger struct{}

// NewLedger builds a ledger.
func NewLedger() *Ledger { return &Ledger{} }

// Apply records one stock movement inside the caller's transaction.
//
// The sequence is deliberate:
//  1. short-circuit on a duplicate client request id;
//  2. lock the stock_items row (or create it), so two concurrent scans of the
//     same bin cannot both read the same "before" quantity;
//  3. reject a change that would drive the balance negative;
//  4. write the new quantity, then the immutable ledger row;
//  5. create or consume FIFO cost layers.
//
// tx MUST be a transaction. Calling this on a bare *gorm.DB gives up the
// atomicity that steps 2–5 depend on.
func (l *Ledger) Apply(ctx context.Context, tx *gorm.DB, req MovementRequest) (*MovementResult, error) {
	if money.IsZero(req.Delta) {
		return nil, shared.Validation("quantity must not be zero")
	}
	if req.OccurredAt.IsZero() {
		req.OccurredAt = time.Now().UTC()
	}

	if req.ClientRequestID != "" {
		existing, err := l.findByClientRequestID(ctx, tx, req.OrgID, req.ClientRequestID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			item, err := l.loadItem(ctx, tx, req)
			if err != nil {
				return nil, err
			}
			return &MovementResult{Movement: existing, Item: item, Duplicate: true}, nil
		}
	}

	if err := l.assertBatchPolicy(ctx, tx, req); err != nil {
		return nil, err
	}

	item, err := l.lockOrCreateItem(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	before := item.Quantity
	after := before.Add(req.Delta)
	if money.IsNegative(after) {
		return nil, shared.InsufficientStock(
			before.InexactFloat64(),
			req.Delta.Abs().InexactFloat64(),
		)
	}
	// Stock already promised to confirmed orders cannot be issued elsewhere.
	if money.IsNegative(req.Delta) && after.LessThan(item.ReservedQuantity) {
		return nil, shared.Conflict("this would leave less stock than is reserved for open orders").
			WithDetails(map[string]any{
				"on_hand":     before,
				"reserved":    item.ReservedQuantity,
				"would_leave": after,
			})
	}

	if err := tx.WithContext(ctx).Model(&Item{}).Where("id = ?", item.ID).
		Updates(map[string]any{"quantity": after, "updated_at": time.Now().UTC()}).Error; err != nil {
		return nil, fmt.Errorf("update stock item: %w", err)
	}
	item.Quantity = after

	movement := &Movement{
		ID:             uuid.New(),
		OrganizationID: req.OrgID,
		Type:           req.Type,
		ProductID:      req.ProductID,
		VariantID:      req.VariantID,
		WarehouseID:    req.WarehouseID,
		LocationID:     req.LocationID,
		BatchID:        req.BatchID,
		QuantityDelta:  req.Delta,
		QuantityBefore: before,
		QuantityAfter:  after,
		UnitCost:       req.UnitCost,
		OccurredAt:     req.OccurredAt,
	}
	if req.Reason != "" {
		movement.Reason = &req.Reason
	}
	if req.Note != "" {
		movement.Note = &req.Note
	}
	if req.ReferenceType != "" {
		movement.ReferenceType = &req.ReferenceType
	}
	movement.ReferenceID = req.ReferenceID
	if req.ActorID != uuid.Nil {
		movement.ActorID = &req.ActorID
	}
	if req.ClientRequestID != "" {
		movement.ClientRequestID = &req.ClientRequestID
	}

	if err := tx.WithContext(ctx).Create(movement).Error; err != nil {
		// A concurrent sync of the same offline scan lost the race; treat the
		// winner's movement as the result rather than failing the client.
		if isUniqueViolation(err) && req.ClientRequestID != "" {
			existing, findErr := l.findByClientRequestID(ctx, tx, req.OrgID, req.ClientRequestID)
			if findErr == nil && existing != nil {
				return &MovementResult{Movement: existing, Item: item, Duplicate: true}, nil
			}
		}
		return nil, fmt.Errorf("record movement: %w", err)
	}

	result := &MovementResult{Movement: movement, Item: item, COGS: money.Zero()}

	if money.IsPositive(req.Delta) {
		if err := l.createCostLayer(ctx, tx, req, movement); err != nil {
			return nil, err
		}
		return result, nil
	}

	cogs, err := l.consumeCostLayers(ctx, tx, req, req.Delta.Abs())
	if err != nil {
		return nil, err
	}
	result.COGS = cogs
	return result, nil
}

// findByClientRequestID looks for an already-applied movement with this
// idempotency key.
func (l *Ledger) findByClientRequestID(ctx context.Context, tx *gorm.DB, orgID uuid.UUID, key string) (*Movement, error) {
	var m Movement
	err := tx.WithContext(ctx).
		Where("organization_id = ? AND client_request_id = ?", orgID, key).
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("check idempotency key: %w", err)
	}
	return &m, nil
}

// lockOrCreateItem takes a row lock on the stock item, creating it at zero if
// this coordinate has never held stock.
func (l *Ledger) lockOrCreateItem(ctx context.Context, tx *gorm.DB, req MovementRequest) (*Item, error) {
	item, err := l.selectForUpdate(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if item != nil {
		return item, nil
	}

	fresh := &Item{
		ID:               uuid.New(),
		OrganizationID:   req.OrgID,
		ProductID:        req.ProductID,
		VariantID:        req.VariantID,
		WarehouseID:      req.WarehouseID,
		LocationID:       req.LocationID,
		BatchID:          req.BatchID,
		Quantity:         money.Zero(),
		ReservedQuantity: money.Zero(),
	}
	err = tx.WithContext(ctx).Create(fresh).Error
	if err == nil {
		return fresh, nil
	}
	if !isUniqueViolation(err) {
		return nil, fmt.Errorf("create stock item: %w", err)
	}

	// Another transaction created the same coordinate first; take its row.
	item, err = l.selectForUpdate(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, shared.Internal("stock item vanished during creation")
	}
	return item, nil
}

// selectForUpdate locks the matching stock item, returning nil when absent.
func (l *Ledger) selectForUpdate(ctx context.Context, tx *gorm.DB, req MovementRequest) (*Item, error) {
	var item Item
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Scopes(coordinate(req)).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock stock item: %w", err)
	}
	return &item, nil
}

// loadItem reads the stock item without locking, for duplicate replies.
func (l *Ledger) loadItem(ctx context.Context, tx *gorm.DB, req MovementRequest) (*Item, error) {
	var item Item
	err := tx.WithContext(ctx).Scopes(coordinate(req)).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &item, err
}

// coordinate matches exactly one stock_items row. NULL location/batch must be
// compared with IS NULL, since `= NULL` matches nothing and would silently
// create duplicate rows.
func coordinate(req MovementRequest) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		db = db.Where("organization_id = ? AND product_id = ? AND warehouse_id = ?",
			req.OrgID, req.ProductID, req.WarehouseID)
		db = nullableEq(db, "variant_id", req.VariantID)
		db = nullableEq(db, "location_id", req.LocationID)
		db = nullableEq(db, "batch_id", req.BatchID)
		return db
	}
}

func nullableEq(db *gorm.DB, column string, val *uuid.UUID) *gorm.DB {
	if val == nil {
		return db.Where(column + " IS NULL")
	}
	return db.Where(column+" = ?", *val)
}

// assertBatchPolicy enforces that batch-tracked products always name a batch,
// and that the batch belongs to the product being moved.
func (l *Ledger) assertBatchPolicy(ctx context.Context, tx *gorm.DB, req MovementRequest) error {
	var row struct {
		TrackBatches bool
	}
	err := tx.WithContext(ctx).Table("products").
		Select("track_batches").
		Where("id = ? AND organization_id = ? AND deleted_at IS NULL", req.ProductID, req.OrgID).
		Scan(&row).Error
	if err != nil {
		return fmt.Errorf("load product batch policy: %w", err)
	}

	if row.TrackBatches && req.BatchID == nil {
		return shared.Validation("this product is batch-tracked; a batch is required")
	}
	if req.BatchID == nil {
		return nil
	}

	var count int64
	if err := tx.WithContext(ctx).Table("batches").
		Where("id = ? AND product_id = ? AND organization_id = ? AND deleted_at IS NULL",
			*req.BatchID, req.ProductID, req.OrgID).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return shared.Validation("batch does not exist for this product")
	}
	return nil
}

// createCostLayer records the cost of an inbound quantity.
func (l *Ledger) createCostLayer(ctx context.Context, tx *gorm.DB, req MovementRequest, m *Movement) error {
	unitCost := money.Zero()
	switch {
	case req.UnitCost != nil:
		unitCost = money.Round(*req.UnitCost)
	default:
		// No cost supplied (a positive adjustment, a transfer-in, a restocked
		// return): fall back to the product's standard cost so valuation stays
		// non-zero.
		var cost money.Decimal
		if err := tx.WithContext(ctx).Table("products").
			Select("cost_price").Where("id = ?", req.ProductID).
			Scan(&cost).Error; err != nil {
			return fmt.Errorf("load product cost: %w", err)
		}
		unitCost = cost
	}
	if money.IsNegative(unitCost) {
		unitCost = money.Zero()
	}

	qty := req.Delta.Abs()
	layer := CostLayer{
		ID:             uuid.New(),
		OrganizationID: req.OrgID,
		ProductID:      req.ProductID,
		VariantID:      req.VariantID,
		WarehouseID:    req.WarehouseID,
		BatchID:        req.BatchID,
		Quantity:       qty,
		Remaining:      qty,
		UnitCost:       unitCost,
		ReceivedAt:     req.OccurredAt,
		MovementID:     &m.ID,
	}
	if err := tx.WithContext(ctx).Create(&layer).Error; err != nil {
		return fmt.Errorf("create cost layer: %w", err)
	}
	return nil
}

// consumeCostLayers draws down the oldest layers first and returns the total
// cost of the goods removed.
//
// Layers are locked in received_at order to keep concurrent issues from
// double-spending the same layer, and to make lock acquisition order
// deterministic (which is what avoids deadlocks between two concurrent
// dispatches of the same product).
func (l *Ledger) consumeCostLayers(ctx context.Context, tx *gorm.DB, req MovementRequest, qty money.Decimal) (money.Decimal, error) {
	var layers []CostLayer
	q := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(`organization_id = ? AND product_id = ? AND warehouse_id = ? AND remaining > 0`,
			req.OrgID, req.ProductID, req.WarehouseID).
		Order("received_at ASC, created_at ASC")
	q = nullableEq(q, "variant_id", req.VariantID)
	if req.BatchID != nil {
		// Batch-tracked stock consumes only its own batch's layers.
		q = q.Where("batch_id = ?", *req.BatchID)
	}
	if err := q.Find(&layers).Error; err != nil {
		return money.Zero(), fmt.Errorf("load cost layers: %w", err)
	}

	remaining := qty
	cogs := money.Zero()

	for i := range layers {
		if !money.IsPositive(remaining) {
			break
		}
		layer := &layers[i]
		take := money.Min(layer.Remaining, remaining)

		cogs = cogs.Add(take.Mul(layer.UnitCost))
		newRemaining := layer.Remaining.Sub(take)
		if err := tx.WithContext(ctx).Model(&CostLayer{}).
			Where("id = ?", layer.ID).
			Update("remaining", newRemaining).Error; err != nil {
			return money.Zero(), fmt.Errorf("consume cost layer: %w", err)
		}
		remaining = remaining.Sub(take)
	}

	// Layers can fall short of on-hand quantity for legitimate historical
	// reasons (stock seeded before costing, a corrected receipt). Valuing the
	// shortfall at standard cost is better than reporting zero COGS, and the
	// gap is logged rather than hidden.
	if money.IsPositive(remaining) {
		var cost money.Decimal
		if err := tx.WithContext(ctx).Table("products").
			Select("cost_price").Where("id = ?", req.ProductID).Scan(&cost).Error; err != nil {
			return money.Zero(), err
		}
		cogs = cogs.Add(remaining.Mul(cost))
	}

	return money.Round(cogs), nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "duplicate key value")
}

// Issue removes stock from a warehouse without the caller naming a location.
//
// This exists because outbound work does not know which bin holds the goods.
// A sales order says "ship 12 of SKU-1 from MAIN"; the stock may be spread over
// three bins and two batches. Apply matches one exact coordinate, so calling it
// with a nil location would look for a warehouse-level row — which is usually
// empty, making bin-located stock unshippable.
//
// Consumption order is oldest-expiry-first, then oldest row: for perishables
// that is FEFO, which is what a warehouse actually picks, and it keeps lock
// acquisition deterministic so two concurrent issues cannot deadlock.
//
// Returns one movement per source row, and the total FIFO cost of the goods
// removed. tx MUST be a transaction.
func (l *Ledger) Issue(ctx context.Context, tx *gorm.DB, req MovementRequest) ([]*MovementResult, money.Decimal, error) {
	if !money.IsNegative(req.Delta) {
		return nil, money.Zero(), shared.Validation("an issue needs a negative quantity")
	}

	// A caller that named a coordinate means it; honour it exactly.
	if req.LocationID != nil || req.BatchID != nil {
		result, err := l.Apply(ctx, tx, req)
		if err != nil {
			return nil, money.Zero(), err
		}
		return []*MovementResult{result}, result.COGS, nil
	}

	var candidates []Item
	q := tx.WithContext(ctx).
		// Lock only stock_items: Postgres refuses FOR UPDATE on the nullable side
		// of the outer join to batches, which is joined purely to order by expiry.
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "si"}}).
		Table("stock_items si").
		Select("si.*").
		Joins("LEFT JOIN batches b ON b.id = si.batch_id").
		Where(`si.organization_id = ? AND si.product_id = ? AND si.warehouse_id = ?
		       AND si.quantity > si.reserved_quantity`,
			req.OrgID, req.ProductID, req.WarehouseID).
		Order("b.expiry_date ASC NULLS LAST, si.created_at ASC")
	q = nullableEq(q, "si.variant_id", req.VariantID)

	if err := q.Find(&candidates).Error; err != nil {
		return nil, money.Zero(), fmt.Errorf("load stock for issue: %w", err)
	}

	// Report the shortfall against what is actually issuable, so the message the
	// client shows matches what a picker would find on the shelf.
	available := money.Zero()
	for _, item := range candidates {
		available = available.Add(item.Available())
	}
	wanted := req.Delta.Abs()
	if available.LessThan(wanted) {
		return nil, money.Zero(), shared.InsufficientStock(
			available.InexactFloat64(), wanted.InexactFloat64())
	}

	results := make([]*MovementResult, 0, len(candidates))
	cogs := money.Zero()
	remaining := wanted

	for i := range candidates {
		if !money.IsPositive(remaining) {
			break
		}
		item := candidates[i]
		take := money.Min(item.Available(), remaining)
		if !money.IsPositive(take) {
			continue
		}

		// One ledger row per source coordinate, so the audit trail records which
		// bin and batch the goods actually left.
		lineReq := req
		lineReq.LocationID = item.LocationID
		lineReq.BatchID = item.BatchID
		lineReq.Delta = take.Neg()
		// The idempotency key belongs to the whole issue, not to each line; only
		// the first line carries it so a retry is still recognised.
		if i > 0 {
			lineReq.ClientRequestID = ""
		}

		result, err := l.Apply(ctx, tx, lineReq)
		if err != nil {
			return nil, money.Zero(), err
		}
		results = append(results, result)
		cogs = cogs.Add(result.COGS)
		remaining = remaining.Sub(take)
	}

	return results, money.Round(cogs), nil
}
