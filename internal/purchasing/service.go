package purchasing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/internal/stock"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// Notifier raises purchasing notifications.
type Notifier interface {
	POPendingApproval(ctx context.Context, orgID, poID uuid.UUID, code string, total money.Decimal)
	PODecided(ctx context.Context, orgID, poID uuid.UUID, code string, approved bool, requesterID *uuid.UUID, reason string)
}

// Service holds purchasing business logic.
type Service struct {
	db       *gorm.DB
	stock    *stock.Service
	audit    *shared.Auditor
	seq      *shared.Sequencer
	notifier Notifier
	lg       zerolog.Logger
}

// NewService builds the purchasing service.
func NewService(
	db *gorm.DB,
	stockSvc *stock.Service,
	audit *shared.Auditor,
	seq *shared.Sequencer,
	notifier Notifier,
	lg zerolog.Logger,
) *Service {
	return &Service{
		db: db, stock: stockSvc, audit: audit, seq: seq, notifier: notifier,
		lg: lg.With().Str("module", "purchasing").Logger(),
	}
}

// --- suppliers -------------------------------------------------------------

// SupplierInput creates or replaces a supplier.
type SupplierInput struct {
	Code         string  `json:"code"          validate:"required,min=1,max=32"`
	Name         string  `json:"name"          validate:"required,min=1,max=200"`
	ContactName  *string `json:"contact_name"  validate:"omitempty,max=120"`
	Email        *string `json:"email"         validate:"omitempty,email,max=255"`
	Phone        *string `json:"phone"         validate:"omitempty,max=32"`
	Address      *string `json:"address"       validate:"omitempty,max=500"`
	TaxNumber    *string `json:"tax_number"    validate:"omitempty,max=64"`
	PaymentTerms *string `json:"payment_terms" validate:"omitempty,max=120"`
	LeadTimeDays int     `json:"lead_time_days" validate:"gte=0,lte=365"`
	IsActive     *bool   `json:"is_active"`
	Notes        *string `json:"notes"         validate:"omitempty,max=2000"`
}

// CreateSupplier adds a supplier.
func (s *Service) CreateSupplier(ctx context.Context, actor auth.Identity, in SupplierInput, meta shared.Actor) (*Supplier, error) {
	sup := Supplier{
		Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		ContactName: in.ContactName, Email: in.Email, Phone: in.Phone,
		Address: in.Address, TaxNumber: in.TaxNumber, PaymentTerms: in.PaymentTerms,
		LeadTimeDays: in.LeadTimeDays, Notes: in.Notes,
		IsActive: in.IsActive == nil || *in.IsActive,
	}
	sup.OrganizationID = actor.OrgID

	if err := s.db.WithContext(ctx).Create(&sup).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a supplier with this code already exists")
		}
		return nil, fmt.Errorf("create supplier: %w", err)
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "supplier.create", EntityType: "supplier", EntityID: &sup.ID, After: sup,
	})
	return &sup, nil
}

// UpdateSupplier replaces a supplier's editable fields.
func (s *Service) UpdateSupplier(ctx context.Context, actor auth.Identity, id uuid.UUID, in SupplierInput, meta shared.Actor) (*Supplier, error) {
	sup, err := s.GetSupplier(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	before := *sup

	sup.Code, sup.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	sup.ContactName, sup.Email, sup.Phone = in.ContactName, in.Email, in.Phone
	sup.Address, sup.TaxNumber, sup.PaymentTerms = in.Address, in.TaxNumber, in.PaymentTerms
	sup.LeadTimeDays, sup.Notes = in.LeadTimeDays, in.Notes
	if in.IsActive != nil {
		sup.IsActive = *in.IsActive
	}

	if err := s.db.WithContext(ctx).Save(sup).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a supplier with this code already exists")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "supplier.update", EntityType: "supplier", EntityID: &id, Before: before, After: *sup,
	})
	return sup, nil
}

// GetSupplier loads one supplier.
func (s *Service) GetSupplier(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Supplier, error) {
	var sup Supplier
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", id).First(&sup).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("supplier")
	}
	return &sup, err
}

// ListSuppliers returns a page of suppliers.
func (s *Service) ListSuppliers(ctx context.Context, actor auth.Identity, q shared.Query) ([]Supplier, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Supplier{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(map[string]string{"is_active": "is_active"}))
	if q.Search != "" {
		like := "%" + strings.ToLower(q.Search) + "%"
		tx = tx.Where("lower(name) LIKE ? OR lower(code) LIKE ?", like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []Supplier
	err := tx.Scopes(
		q.OrderBy(map[string]string{"name": "name", "code": "code", "created_at": "created_at"}, "name ASC"),
		q.Paginate(),
	).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// DeleteSupplier soft-deletes a supplier, refusing while open POs reference it.
func (s *Service) DeleteSupplier(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) error {
	var open int64
	err := s.db.WithContext(ctx).Model(&PurchaseOrder{}).
		Where("supplier_id = ? AND status NOT IN ?", id,
			[]Status{StatusReceived, StatusCancelled, StatusRejected}).
		Count(&open).Error
	if err != nil {
		return err
	}
	if open > 0 {
		return shared.Conflict("this supplier has open purchase orders")
	}

	res := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Delete(&Supplier{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return shared.NotFound("supplier")
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "supplier.delete", EntityType: "supplier", EntityID: &id,
	})
	return nil
}

// --- purchase orders -------------------------------------------------------

// POInput drafts a purchase order.
type POInput struct {
	SupplierID    uuid.UUID     `json:"supplier_id"   validate:"required"`
	WarehouseID   uuid.UUID     `json:"warehouse_id"  validate:"required"`
	Currency      string        `json:"currency"      validate:"omitempty,len=3"`
	ExpectedDate  *time.Time    `json:"expected_date"`
	ShippingTotal money.Decimal `json:"shipping_total"`
	Note          string        `json:"note"          validate:"omitempty,max=2000"`
	Lines         []POLineInput `json:"lines"         validate:"required,min=1,dive"`
}

// POLineInput is one ordered product.
type POLineInput struct {
	ProductID    uuid.UUID     `json:"product_id"  validate:"required"`
	VariantID    *uuid.UUID    `json:"variant_id"`
	Description  *string       `json:"description" validate:"omitempty,max=500"`
	Quantity     money.Decimal `json:"quantity"`
	UnitPrice    money.Decimal `json:"unit_price"`
	TaxRate      money.Decimal `json:"tax_rate"`
	DiscountRate money.Decimal `json:"discount_rate"`
}

// CreatePO drafts a purchase order and computes its totals.
func (s *Service) CreatePO(ctx context.Context, actor auth.Identity, in POInput, meta shared.Actor) (*PurchaseOrder, error) {
	if _, err := s.GetSupplier(ctx, actor, in.SupplierID); err != nil {
		return nil, err
	}

	po := PurchaseOrder{
		SupplierID: in.SupplierID, WarehouseID: in.WarehouseID,
		Status: StatusDraft, Currency: currencyOr(in.Currency),
		ExpectedDate: in.ExpectedDate, ShippingTotal: money.Round(in.ShippingTotal),
	}
	po.OrganizationID = actor.OrgID
	if in.Note != "" {
		po.Note = &in.Note
	}
	if actor.UserID != uuid.Nil {
		id := actor.UserID
		po.CreatedBy = &id
	}

	lines, subtotal, tax, discount, err := buildPOLines(in.Lines)
	if err != nil {
		return nil, err
	}
	po.Subtotal, po.TaxTotal, po.DiscountTotal = subtotal, tax, discount
	po.GrandTotal = money.Round(subtotal.Add(tax).Add(po.ShippingTotal))

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "purchase_order", "PO")
		if err != nil {
			return err
		}
		po.Code = code
		if err := tx.Create(&po).Error; err != nil {
			return fmt.Errorf("create purchase order: %w", err)
		}
		for i := range lines {
			lines[i].PurchaseOrderID = po.ID
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create purchase order lines: %w", err)
		}
		po.Lines = lines
		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "purchase_order.create", EntityType: "purchase_order", EntityID: &po.ID, After: po,
		})
	})
	if err != nil {
		return nil, err
	}
	return &po, nil
}

// buildPOLines computes line totals and the document subtotals.
func buildPOLines(inputs []POLineInput) (lines []PurchaseOrderLine, subtotal, tax, discount money.Decimal, err error) {
	subtotal, tax, discount = money.Zero(), money.Zero(), money.Zero()
	lines = make([]PurchaseOrderLine, 0, len(inputs))

	for _, l := range inputs {
		if !money.IsPositive(l.Quantity) {
			return nil, subtotal, tax, discount, shared.Validation("every line needs a positive quantity")
		}
		if money.IsNegative(l.UnitPrice) {
			return nil, subtotal, tax, discount, shared.Validation("unit price cannot be negative")
		}

		net, lineTax, total := money.LineTotal(l.Quantity, l.UnitPrice, l.DiscountRate, l.TaxRate)
		gross := money.Round(l.Quantity.Mul(l.UnitPrice))

		lines = append(lines, PurchaseOrderLine{
			ID: uuid.New(), ProductID: l.ProductID, VariantID: l.VariantID,
			Description:     l.Description,
			QuantityOrdered: money.Round(l.Quantity), QuantityReceived: money.Zero(),
			UnitPrice: money.Round(l.UnitPrice),
			TaxRate:   l.TaxRate, DiscountRate: l.DiscountRate,
			LineTotal: total,
		})
		subtotal = subtotal.Add(net)
		tax = tax.Add(lineTax)
		discount = discount.Add(gross.Sub(net))
	}
	return lines, money.Round(subtotal), money.Round(tax), money.Round(discount), nil
}

// SubmitPO moves a draft into the approval queue.
func (s *Service) SubmitPO(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) (*PurchaseOrder, error) {
	po, err := s.transition(ctx, actor, id, StatusPendingApproval, func(tx *gorm.DB, po *PurchaseOrder) (map[string]any, error) {
		now := time.Now().UTC()
		po.SubmittedAt = &now
		return map[string]any{"status": StatusPendingApproval, "submitted_at": now}, nil
	}, meta, "purchase_order.submit")
	if err != nil {
		return nil, err
	}
	s.notifier.POPendingApproval(ctx, actor.OrgID, po.ID, po.Code, po.GrandTotal)
	return po, nil
}

// ApprovePO authorises a submitted order.
//
// Approval is a separate permission from creation, and a user cannot approve
// their own order — the classic segregation-of-duties control on spend.
func (s *Service) ApprovePO(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) (*PurchaseOrder, error) {
	po, err := s.transition(ctx, actor, id, StatusApproved, func(tx *gorm.DB, po *PurchaseOrder) (map[string]any, error) {
		if po.CreatedBy != nil && *po.CreatedBy == actor.UserID && actor.Role != auth.RoleAdmin {
			return nil, shared.Forbidden("you cannot approve a purchase order you raised")
		}
		now := time.Now().UTC()
		po.ApprovedAt, po.ApprovedBy = &now, &actor.UserID
		return map[string]any{"status": StatusApproved, "approved_at": now, "approved_by": actor.UserID}, nil
	}, meta, "purchase_order.approve")
	if err != nil {
		return nil, err
	}
	s.notifier.PODecided(ctx, actor.OrgID, po.ID, po.Code, true, po.CreatedBy, "")
	return po, nil
}

// RejectInput carries the rejection reason.
type RejectInput struct {
	Reason string `json:"reason" validate:"required,min=3,max=500"`
}

// RejectPO declines a submitted order.
func (s *Service) RejectPO(ctx context.Context, actor auth.Identity, id uuid.UUID, in RejectInput, meta shared.Actor) (*PurchaseOrder, error) {
	po, err := s.transition(ctx, actor, id, StatusRejected, func(tx *gorm.DB, po *PurchaseOrder) (map[string]any, error) {
		now := time.Now().UTC()
		po.RejectedAt, po.RejectedBy, po.RejectionReason = &now, &actor.UserID, &in.Reason
		return map[string]any{
			"status": StatusRejected, "rejected_at": now,
			"rejected_by": actor.UserID, "rejection_reason": in.Reason,
		}, nil
	}, meta, "purchase_order.reject")
	if err != nil {
		return nil, err
	}
	s.notifier.PODecided(ctx, actor.OrgID, po.ID, po.Code, false, po.CreatedBy, in.Reason)
	return po, nil
}

// CancelPO voids an order that has not been fully received.
func (s *Service) CancelPO(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) (*PurchaseOrder, error) {
	return s.transition(ctx, actor, id, StatusCancelled, func(tx *gorm.DB, po *PurchaseOrder) (map[string]any, error) {
		return map[string]any{"status": StatusCancelled}, nil
	}, meta, "purchase_order.cancel")
}

// transition applies a guarded status change to a purchase order.
func (s *Service) transition(
	ctx context.Context,
	actor auth.Identity,
	id uuid.UUID,
	next Status,
	apply func(*gorm.DB, *PurchaseOrder) (map[string]any, error),
	meta shared.Actor,
	action string,
) (*PurchaseOrder, error) {
	var po PurchaseOrder
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", id).First(&po).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("purchase order")
			}
			return err
		}
		if !po.Status.CanTransitionTo(next) {
			return shared.InvalidTransition(string(po.Status), string(next))
		}

		updates, err := apply(tx, &po)
		if err != nil {
			return err
		}
		before := po.Status
		if err := tx.Model(&PurchaseOrder{}).Where("id = ?", po.ID).Updates(updates).Error; err != nil {
			return err
		}
		po.Status = next

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: action, EntityType: "purchase_order", EntityID: &po.ID,
			Before: map[string]any{"status": before},
			After:  map[string]any{"status": next},
		})
	})
	if err != nil {
		return nil, err
	}
	return &po, nil
}

// GetPO loads an order with lines and supplier.
func (s *Service) GetPO(ctx context.Context, actor auth.Identity, id uuid.UUID) (*PurchaseOrder, error) {
	var po PurchaseOrder
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Preload("Supplier").Where("id = ?", id).First(&po).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("purchase order")
	}
	return &po, err
}

// ListPOs returns a page of purchase orders.
func (s *Service) ListPOs(ctx context.Context, actor auth.Identity, q shared.Query) ([]PurchaseOrder, shared.PageMeta, error) {
	filterable := map[string]string{
		"status": "purchase_orders.status", "supplier_id": "purchase_orders.supplier_id",
		"warehouse_id": "purchase_orders.warehouse_id",
	}
	tx := s.db.WithContext(ctx).Model(&PurchaseOrder{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))
	if q.Search != "" {
		tx = tx.Where("purchase_orders.code ILIKE ?", "%"+q.Search+"%")
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	sortable := map[string]string{
		"created_at": "purchase_orders.created_at", "code": "purchase_orders.code",
		"grand_total": "purchase_orders.grand_total", "expected_date": "purchase_orders.expected_date",
	}
	var rows []PurchaseOrder
	err := tx.Preload("Supplier").
		Scopes(q.OrderBy(sortable, "purchase_orders.created_at DESC"), q.Paginate()).
		Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- goods receipts --------------------------------------------------------

// ReceiveInput records a delivery against an approved purchase order.
type ReceiveInput struct {
	SupplierRef string             `json:"supplier_ref" validate:"omitempty,max=64"`
	Note        string             `json:"note"         validate:"omitempty,max=2000"`
	Lines       []ReceiveLineInput `json:"lines"        validate:"required,min=1,dive"`
}

// ReceiveLineInput is one received quantity against a PO line.
type ReceiveLineInput struct {
	PurchaseOrderLineID uuid.UUID      `json:"purchase_order_line_id" validate:"required"`
	Quantity            money.Decimal  `json:"quantity"`
	UnitCost            *money.Decimal `json:"unit_cost"`
	BatchID             *uuid.UUID     `json:"batch_id"`
	LocationID          *uuid.UUID     `json:"location_id"`
}

// Receive books a (possibly partial) delivery.
//
// The whole receipt is one transaction: stock movements, FIFO layers, line
// quantities and the PO status all commit together, so a failure halfway
// through cannot leave stock on hand that the PO does not know it received.
func (s *Service) Receive(ctx context.Context, actor auth.Identity, poID uuid.UUID, in ReceiveInput, meta shared.Actor) (*GoodsReceipt, error) {
	var grn GoodsReceipt

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var po PurchaseOrder
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", poID).First(&po).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("purchase order")
			}
			return err
		}
		if po.Status != StatusApproved && po.Status != StatusPartiallyReceived {
			return shared.Conflict("only an approved purchase order can be received").
				WithDetails(map[string]any{"status": po.Status})
		}

		linesByID := make(map[uuid.UUID]*PurchaseOrderLine, len(po.Lines))
		for i := range po.Lines {
			linesByID[po.Lines[i].ID] = &po.Lines[i]
		}

		code, err := s.seq.Next(ctx, tx, actor.OrgID, "goods_receipt", "GRN")
		if err != nil {
			return err
		}
		grn = GoodsReceipt{
			Code: code, PurchaseOrderID: po.ID, WarehouseID: po.WarehouseID,
			ReceivedAt: time.Now().UTC(),
		}
		grn.OrganizationID = actor.OrgID
		if in.SupplierRef != "" {
			grn.SupplierRef = &in.SupplierRef
		}
		if in.Note != "" {
			grn.Note = &in.Note
		}
		if actor.UserID != uuid.Nil {
			id := actor.UserID
			grn.ReceivedBy = &id
		}
		if err := tx.Create(&grn).Error; err != nil {
			return fmt.Errorf("create goods receipt: %w", err)
		}

		grnLines := make([]GoodsReceiptLine, 0, len(in.Lines))
		for _, l := range in.Lines {
			poLine, ok := linesByID[l.PurchaseOrderLineID]
			if !ok {
				return shared.Validation("line does not belong to this purchase order").
					WithDetails(map[string]any{"purchase_order_line_id": l.PurchaseOrderLineID})
			}
			if !money.IsPositive(l.Quantity) {
				return shared.Validation("received quantity must be positive")
			}
			outstanding := poLine.Outstanding()
			if l.Quantity.GreaterThan(outstanding) {
				return shared.Conflict("received quantity exceeds what is outstanding on this line").
					WithDetails(map[string]any{
						"purchase_order_line_id": poLine.ID,
						"outstanding":            outstanding,
						"received":               l.Quantity,
					})
			}

			// Cost precedence: what actually arrived on the invoice, else the
			// agreed PO price.
			unitCost := poLine.UnitPrice
			if l.UnitCost != nil {
				unitCost = money.Round(*l.UnitCost)
			}

			if _, err := s.stock.ApplyInTx(ctx, tx, stock.MovementRequest{
				OrgID: actor.OrgID, Type: stock.MovementIn,
				ProductID: poLine.ProductID, VariantID: poLine.VariantID,
				WarehouseID: po.WarehouseID, LocationID: l.LocationID, BatchID: l.BatchID,
				Delta: money.Round(l.Quantity), UnitCost: &unitCost,
				Reason:        "goods receipt " + grn.Code,
				ReferenceType: "goods_receipt", ReferenceID: &grn.ID,
				ActorID: actor.UserID,
			}); err != nil {
				return err
			}

			newReceived := poLine.QuantityReceived.Add(money.Round(l.Quantity))
			if err := tx.Model(&PurchaseOrderLine{}).Where("id = ?", poLine.ID).
				Update("quantity_received", newReceived).Error; err != nil {
				return err
			}
			poLine.QuantityReceived = newReceived

			grnLines = append(grnLines, GoodsReceiptLine{
				ID: uuid.New(), GoodsReceiptID: grn.ID, PurchaseOrderLineID: poLine.ID,
				ProductID: poLine.ProductID, VariantID: poLine.VariantID,
				BatchID: l.BatchID, LocationID: l.LocationID,
				Quantity: money.Round(l.Quantity), UnitCost: unitCost,
			})
		}
		if err := tx.Create(&grnLines).Error; err != nil {
			return fmt.Errorf("create goods receipt lines: %w", err)
		}
		grn.Lines = grnLines

		// Fully received only when every line is satisfied.
		nextStatus := StatusReceived
		for _, line := range po.Lines {
			if money.IsPositive(line.Outstanding()) {
				nextStatus = StatusPartiallyReceived
				break
			}
		}
		if err := tx.Model(&PurchaseOrder{}).Where("id = ?", po.ID).
			Update("status", nextStatus).Error; err != nil {
			return err
		}

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "purchase_order.receive", EntityType: "goods_receipt", EntityID: &grn.ID,
			After: map[string]any{"purchase_order_id": po.ID, "status": nextStatus, "code": grn.Code},
		})
	})
	if err != nil {
		return nil, err
	}
	return &grn, nil
}

// ListReceipts returns the goods receipts for a purchase order.
func (s *Service) ListReceipts(ctx context.Context, actor auth.Identity, poID uuid.UUID) ([]GoodsReceipt, error) {
	var rows []GoodsReceipt
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Where("purchase_order_id = ?", poID).
		Order("received_at DESC").Find(&rows).Error
	return rows, err
}

func currencyOr(c string) string {
	if c == "" {
		return "USD"
	}
	return strings.ToUpper(c)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
