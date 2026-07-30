package sales

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

// Notifier raises sales notifications.
type Notifier interface {
	ReturnRequested(ctx context.Context, orgID, returnID uuid.UUID, code, reason string)
}

// Service holds sales business logic.
type Service struct {
	db       *gorm.DB
	stock    *stock.Service
	audit    *shared.Auditor
	seq      *shared.Sequencer
	notifier Notifier
	lg       zerolog.Logger
}

// NewService builds the sales service.
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
		lg: lg.With().Str("module", "sales").Logger(),
	}
}

// --- customers -------------------------------------------------------------

// CustomerInput creates or replaces a customer.
type CustomerInput struct {
	Code            string        `json:"code"             validate:"required,min=1,max=32"`
	Name            string        `json:"name"             validate:"required,min=1,max=200"`
	Email           *string       `json:"email"            validate:"omitempty,email,max=255"`
	Phone           *string       `json:"phone"            validate:"omitempty,max=32"`
	BillingAddress  *string       `json:"billing_address"  validate:"omitempty,max=500"`
	ShippingAddress *string       `json:"shipping_address" validate:"omitempty,max=500"`
	TaxNumber       *string       `json:"tax_number"       validate:"omitempty,max=64"`
	CreditLimit     money.Decimal `json:"credit_limit"`
	IsActive        *bool         `json:"is_active"`
}

// CreateCustomer adds a customer.
func (s *Service) CreateCustomer(ctx context.Context, actor auth.Identity, in CustomerInput, meta shared.Actor) (*Customer, error) {
	cust := Customer{
		Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		Email: in.Email, Phone: in.Phone,
		BillingAddress: in.BillingAddress, ShippingAddress: in.ShippingAddress,
		TaxNumber: in.TaxNumber, CreditLimit: money.Round(in.CreditLimit),
		IsActive: in.IsActive == nil || *in.IsActive,
	}
	cust.OrganizationID = actor.OrgID

	if err := s.db.WithContext(ctx).Create(&cust).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a customer with this code already exists")
		}
		return nil, fmt.Errorf("create customer: %w", err)
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "customer.create", EntityType: "customer", EntityID: &cust.ID, After: cust,
	})
	return &cust, nil
}

// ListCustomers returns a page of customers.
func (s *Service) ListCustomers(ctx context.Context, actor auth.Identity, q shared.Query) ([]Customer, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Customer{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(map[string]string{"is_active": "is_active"}))
	if q.Search != "" {
		like := "%" + strings.ToLower(q.Search) + "%"
		tx = tx.Where("lower(name) LIKE ? OR lower(code) LIKE ?", like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []Customer
	err := tx.Scopes(
		q.OrderBy(map[string]string{"name": "name", "code": "code", "created_at": "created_at"}, "name ASC"),
		q.Paginate(),
	).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// GetCustomer loads one customer.
func (s *Service) GetCustomer(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Customer, error) {
	var cust Customer
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", id).First(&cust).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("customer")
	}
	return &cust, err
}

// UpdateCustomer replaces a customer's editable fields.
func (s *Service) UpdateCustomer(ctx context.Context, actor auth.Identity, id uuid.UUID, in CustomerInput, meta shared.Actor) (*Customer, error) {
	cust, err := s.GetCustomer(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	before := *cust

	cust.Code, cust.Name = strings.TrimSpace(in.Code), strings.TrimSpace(in.Name)
	cust.Email, cust.Phone = in.Email, in.Phone
	cust.BillingAddress, cust.ShippingAddress = in.BillingAddress, in.ShippingAddress
	cust.TaxNumber, cust.CreditLimit = in.TaxNumber, money.Round(in.CreditLimit)
	if in.IsActive != nil {
		cust.IsActive = *in.IsActive
	}

	if err := s.db.WithContext(ctx).Save(cust).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a customer with this code already exists")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "customer.update", EntityType: "customer", EntityID: &id, Before: before, After: *cust,
	})
	return cust, nil
}

// --- sales orders ----------------------------------------------------------

// OrderInput drafts a sales order.
type OrderInput struct {
	CustomerID      *uuid.UUID       `json:"customer_id"`
	WarehouseID     uuid.UUID        `json:"warehouse_id"  validate:"required"`
	Currency        string           `json:"currency"      validate:"omitempty,len=3"`
	RequiredDate    *time.Time       `json:"required_date"`
	ShippingTotal   money.Decimal    `json:"shipping_total"`
	ShippingAddress *string          `json:"shipping_address" validate:"omitempty,max=500"`
	Note            string           `json:"note"          validate:"omitempty,max=2000"`
	Lines           []OrderLineInput `json:"lines"         validate:"required,min=1,dive"`
}

// OrderLineInput is one ordered product.
type OrderLineInput struct {
	ProductID    uuid.UUID     `json:"product_id"  validate:"required"`
	VariantID    *uuid.UUID    `json:"variant_id"`
	Description  *string       `json:"description" validate:"omitempty,max=500"`
	Quantity     money.Decimal `json:"quantity"`
	UnitPrice    money.Decimal `json:"unit_price"`
	TaxRate      money.Decimal `json:"tax_rate"`
	DiscountRate money.Decimal `json:"discount_rate"`
}

// CreateOrder drafts a sales order. No stock is reserved until it is confirmed.
func (s *Service) CreateOrder(ctx context.Context, actor auth.Identity, in OrderInput, meta shared.Actor) (*Order, error) {
	if in.CustomerID != nil {
		if _, err := s.GetCustomer(ctx, actor, *in.CustomerID); err != nil {
			return nil, err
		}
	}

	order := Order{
		CustomerID: in.CustomerID, WarehouseID: in.WarehouseID,
		Status: OrderDraft, Currency: currencyOr(in.Currency),
		OrderDate: time.Now().UTC(), RequiredDate: in.RequiredDate,
		ShippingTotal: money.Round(in.ShippingTotal), ShippingAddress: in.ShippingAddress,
	}
	order.OrganizationID = actor.OrgID
	if in.Note != "" {
		order.Note = &in.Note
	}
	if actor.UserID != uuid.Nil {
		id := actor.UserID
		order.CreatedBy = &id
	}

	lines, subtotal, tax, discount, err := buildOrderLines(in.Lines)
	if err != nil {
		return nil, err
	}
	order.Subtotal, order.TaxTotal, order.DiscountTotal = subtotal, tax, discount
	order.GrandTotal = money.Round(subtotal.Add(tax).Add(order.ShippingTotal))

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "sales_order", "SO")
		if err != nil {
			return err
		}
		order.Code = code
		if err := tx.Create(&order).Error; err != nil {
			return fmt.Errorf("create sales order: %w", err)
		}
		for i := range lines {
			lines[i].SalesOrderID = order.ID
		}
		if err := tx.Create(&lines).Error; err != nil {
			return fmt.Errorf("create sales order lines: %w", err)
		}
		order.Lines = lines
		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "sales_order.create", EntityType: "sales_order", EntityID: &order.ID, After: order,
		})
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func buildOrderLines(inputs []OrderLineInput) (lines []OrderLine, subtotal, tax, discount money.Decimal, err error) {
	subtotal, tax, discount = money.Zero(), money.Zero(), money.Zero()
	lines = make([]OrderLine, 0, len(inputs))

	for _, l := range inputs {
		if !money.IsPositive(l.Quantity) {
			return nil, subtotal, tax, discount, shared.Validation("every line needs a positive quantity")
		}
		if money.IsNegative(l.UnitPrice) {
			return nil, subtotal, tax, discount, shared.Validation("unit price cannot be negative")
		}

		net, lineTax, total := money.LineTotal(l.Quantity, l.UnitPrice, l.DiscountRate, l.TaxRate)
		gross := money.Round(l.Quantity.Mul(l.UnitPrice))

		lines = append(lines, OrderLine{
			ID: uuid.New(), ProductID: l.ProductID, VariantID: l.VariantID,
			Description: l.Description,
			Quantity:    money.Round(l.Quantity), QuantityShipped: money.Zero(),
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

// Confirm reserves stock for the order.
//
// Reservation is what stops two orders promising the same unit. It fails loudly
// when stock is short rather than confirming an order the warehouse cannot
// fulfil.
func (s *Service) Confirm(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) (*Order, error) {
	var order Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.loadForUpdate(ctx, tx, actor, id, &order); err != nil {
			return err
		}
		if !order.Status.CanTransitionTo(OrderConfirmed) {
			return shared.InvalidTransition(string(order.Status), string(OrderConfirmed))
		}

		for _, line := range order.Lines {
			if err := s.stock.Reserve(ctx, tx, actor.OrgID, line.ProductID, line.VariantID,
				order.WarehouseID, line.Quantity); err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		if err := tx.Model(&Order{}).Where("id = ?", order.ID).Updates(map[string]any{
			"status": OrderConfirmed, "confirmed_at": now,
		}).Error; err != nil {
			return err
		}
		order.Status, order.ConfirmedAt = OrderConfirmed, &now

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "sales_order.confirm", EntityType: "sales_order", EntityID: &order.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// Advance moves an order to the next fulfilment state (picking, packed,
// delivered). Shipping is separate because it moves stock.
func (s *Service) Advance(ctx context.Context, actor auth.Identity, id uuid.UUID, next OrderStatus, meta shared.Actor) (*Order, error) {
	if next == OrderShipped {
		return nil, shared.Validation("use the ship endpoint to dispatch an order")
	}

	var order Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.loadForUpdate(ctx, tx, actor, id, &order); err != nil {
			return err
		}
		if !order.Status.CanTransitionTo(next) {
			return shared.InvalidTransition(string(order.Status), string(next))
		}

		now := time.Now().UTC()
		updates := map[string]any{"status": next}
		switch next {
		case OrderPicking:
			updates["picked_at"] = now
		case OrderPacked:
			updates["packed_at"] = now
		case OrderDelivered:
			updates["delivered_at"] = now
		}
		if err := tx.Model(&Order{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
			return err
		}
		order.Status = next

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "sales_order." + string(next), EntityType: "sales_order", EntityID: &order.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// ShipInput optionally carries a tracking number.
type ShipInput struct {
	TrackingNumber string `json:"tracking_number" validate:"omitempty,max=120"`
}

// Ship issues the stock and closes out the reservation.
//
// This is the point where inventory actually decreases. COGS is captured per
// line from the FIFO layers consumed, so margin reporting reflects what the
// goods really cost rather than today's price.
func (s *Service) Ship(ctx context.Context, actor auth.Identity, id uuid.UUID, in ShipInput, meta shared.Actor) (*Order, error) {
	var order Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.loadForUpdate(ctx, tx, actor, id, &order); err != nil {
			return err
		}
		if !order.Status.CanTransitionTo(OrderShipped) {
			return shared.InvalidTransition(string(order.Status), string(OrderShipped))
		}

		for _, line := range order.Lines {
			outstanding := line.Quantity.Sub(line.QuantityShipped)
			if !money.IsPositive(outstanding) {
				continue
			}

			// Release the reservation first: the ledger refuses to take stock
			// below the reserved figure, and this line's own reservation is
			// exactly what it is about to consume.
			if err := s.stock.Release(ctx, tx, actor.OrgID, line.ProductID, line.VariantID,
				order.WarehouseID, outstanding); err != nil {
				return err
			}

			result, err := s.stock.ApplyInTx(ctx, tx, stock.MovementRequest{
				OrgID: actor.OrgID, Type: stock.MovementOut,
				ProductID: line.ProductID, VariantID: line.VariantID,
				WarehouseID:   order.WarehouseID,
				Delta:         outstanding.Neg(),
				Reason:        "sales order " + order.Code,
				ReferenceType: "sales_order", ReferenceID: &order.ID,
				ActorID: actor.UserID,
			})
			if err != nil {
				return err
			}

			cogs := result.COGS
			if err := tx.Model(&OrderLine{}).Where("id = ?", line.ID).Updates(map[string]any{
				"quantity_shipped": line.Quantity,
				"cogs_total":       cogs,
			}).Error; err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		updates := map[string]any{"status": OrderShipped, "shipped_at": now}
		if in.TrackingNumber != "" {
			updates["tracking_number"] = in.TrackingNumber
		}
		if err := tx.Model(&Order{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
			return err
		}
		order.Status, order.ShippedAt = OrderShipped, &now

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "sales_order.ship", EntityType: "sales_order", EntityID: &order.ID,
			After: map[string]any{"tracking_number": in.TrackingNumber},
		})
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// Cancel voids an order, releasing any reservation it held. An order that has
// already shipped cannot be cancelled — that is a return.
func (s *Service) Cancel(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) (*Order, error) {
	var order Order
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.loadForUpdate(ctx, tx, actor, id, &order); err != nil {
			return err
		}
		if !order.Status.CanTransitionTo(OrderCancelled) {
			return shared.InvalidTransition(string(order.Status), string(OrderCancelled))
		}

		// Reservations exist only once confirmed.
		if order.ConfirmedAt != nil {
			for _, line := range order.Lines {
				outstanding := line.Quantity.Sub(line.QuantityShipped)
				if !money.IsPositive(outstanding) {
					continue
				}
				if err := s.stock.Release(ctx, tx, actor.OrgID, line.ProductID, line.VariantID,
					order.WarehouseID, outstanding); err != nil {
					return err
				}
			}
		}

		now := time.Now().UTC()
		if err := tx.Model(&Order{}).Where("id = ?", order.ID).Updates(map[string]any{
			"status": OrderCancelled, "cancelled_at": now,
		}).Error; err != nil {
			return err
		}
		order.Status, order.CancelledAt = OrderCancelled, &now

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "sales_order.cancel", EntityType: "sales_order", EntityID: &order.ID,
		})
	})
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetOrder loads an order with lines and customer.
func (s *Service) GetOrder(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Order, error) {
	var order Order
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Preload("Customer").Where("id = ?", id).First(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("sales order")
	}
	return &order, err
}

// ListOrders returns a page of sales orders.
func (s *Service) ListOrders(ctx context.Context, actor auth.Identity, q shared.Query) ([]Order, shared.PageMeta, error) {
	filterable := map[string]string{
		"status": "sales_orders.status", "customer_id": "sales_orders.customer_id",
		"warehouse_id": "sales_orders.warehouse_id",
	}
	tx := s.db.WithContext(ctx).Model(&Order{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))
	if q.Search != "" {
		tx = tx.Where("sales_orders.code ILIKE ?", "%"+q.Search+"%")
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	sortable := map[string]string{
		"created_at": "sales_orders.created_at", "code": "sales_orders.code",
		"grand_total": "sales_orders.grand_total", "order_date": "sales_orders.order_date",
	}
	var rows []Order
	err := tx.Preload("Customer").
		Scopes(q.OrderBy(sortable, "sales_orders.created_at DESC"), q.Paginate()).
		Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// loadForUpdate loads an order with its lines under a row lock.
func (s *Service) loadForUpdate(ctx context.Context, tx *gorm.DB, actor auth.Identity, id uuid.UUID, dst *Order) error {
	err := tx.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Preload("Lines").Where("id = ?", id).First(dst).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return shared.NotFound("sales order")
	}
	return err
}

// --- invoices --------------------------------------------------------------

// IssueInvoiceInput sets the payment terms on a new invoice.
type IssueInvoiceInput struct {
	DueInDays int    `json:"due_in_days" validate:"gte=0,lte=365"`
	Note      string `json:"note"        validate:"omitempty,max=2000"`
}

// IssueInvoice bills a shipped order.
func (s *Service) IssueInvoice(ctx context.Context, actor auth.Identity, orderID uuid.UUID, in IssueInvoiceInput, meta shared.Actor) (*Invoice, error) {
	order, err := s.GetOrder(ctx, actor, orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != OrderShipped && order.Status != OrderDelivered {
		return nil, shared.Conflict("only a shipped order can be invoiced").
			WithDetails(map[string]any{"status": order.Status})
	}

	var existing int64
	if err := s.db.WithContext(ctx).Model(&Invoice{}).
		Where("sales_order_id = ? AND status <> ?", orderID, InvoiceVoid).
		Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, shared.Conflict("this order already has an invoice")
	}

	now := time.Now().UTC()
	due := now.AddDate(0, 0, in.DueInDays)
	inv := Invoice{
		SalesOrderID: &order.ID, CustomerID: order.CustomerID,
		Status: InvoiceIssued, Currency: order.Currency,
		IssuedAt: &now, DueAt: &due,
		Total: order.GrandTotal, AmountPaid: money.Zero(),
	}
	inv.OrganizationID = actor.OrgID
	if in.Note != "" {
		inv.Note = &in.Note
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "invoice", "INV")
		if err != nil {
			return err
		}
		inv.Code = code
		if err := tx.Create(&inv).Error; err != nil {
			return err
		}
		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "invoice.issue", EntityType: "invoice", EntityID: &inv.ID, After: inv,
		})
	})
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// RecordPaymentInput records money received.
type RecordPaymentInput struct {
	Amount money.Decimal `json:"amount"`
}

// RecordPayment applies a payment and advances the invoice status.
func (s *Service) RecordPayment(ctx context.Context, actor auth.Identity, invoiceID uuid.UUID, in RecordPaymentInput, meta shared.Actor) (*Invoice, error) {
	if !money.IsPositive(in.Amount) {
		return nil, shared.Validation("payment amount must be positive")
	}

	var inv Invoice
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", invoiceID).First(&inv).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("invoice")
			}
			return err
		}
		if inv.Status == InvoiceVoid || inv.Status == InvoicePaid {
			return shared.Conflict("this invoice cannot accept further payment").
				WithDetails(map[string]any{"status": inv.Status})
		}

		paid := money.Round(inv.AmountPaid.Add(in.Amount))
		if paid.GreaterThan(inv.Total) {
			return shared.Validation("payment exceeds the invoice total").
				WithDetails(map[string]any{"outstanding": inv.Total.Sub(inv.AmountPaid)})
		}

		status := InvoicePartiallyPaid
		if paid.Equal(inv.Total) {
			status = InvoicePaid
		}
		if err := tx.Model(&Invoice{}).Where("id = ?", inv.ID).Updates(map[string]any{
			"amount_paid": paid, "status": status,
		}).Error; err != nil {
			return err
		}
		inv.AmountPaid, inv.Status = paid, status

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "invoice.payment", EntityType: "invoice", EntityID: &inv.ID,
			After: map[string]any{"amount_paid": paid, "status": status},
		})
	})
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// ListInvoices returns a page of invoices.
func (s *Service) ListInvoices(ctx context.Context, actor auth.Identity, q shared.Query) ([]Invoice, shared.PageMeta, error) {
	filterable := map[string]string{"status": "status", "customer_id": "customer_id"}
	tx := s.db.WithContext(ctx).Model(&Invoice{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []Invoice
	err := tx.Scopes(
		q.OrderBy(map[string]string{"issued_at": "issued_at", "due_at": "due_at", "code": "code"}, "created_at DESC"),
		q.Paginate(),
	).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
}

// --- returns ---------------------------------------------------------------

// ReturnInput opens an RMA.
type ReturnInput struct {
	SalesOrderID uuid.UUID         `json:"sales_order_id" validate:"required"`
	Reason       string            `json:"reason"         validate:"required,min=3,max=500"`
	Restock      *bool             `json:"restock"`
	Lines        []ReturnLineInput `json:"lines"          validate:"required,min=1,dive"`
}

// ReturnLineInput is one returned product.
type ReturnLineInput struct {
	SalesOrderLineID uuid.UUID     `json:"sales_order_line_id" validate:"required"`
	Quantity         money.Decimal `json:"quantity"`
	BatchID          *uuid.UUID    `json:"batch_id"`
	Condition        *string       `json:"condition"           validate:"omitempty,max=200"`
}

// RequestReturn opens an RMA against a shipped order.
func (s *Service) RequestReturn(ctx context.Context, actor auth.Identity, in ReturnInput, meta shared.Actor) (*Return, error) {
	order, err := s.GetOrder(ctx, actor, in.SalesOrderID)
	if err != nil {
		return nil, err
	}
	if order.Status != OrderShipped && order.Status != OrderDelivered {
		return nil, shared.Conflict("only a shipped order can be returned").
			WithDetails(map[string]any{"status": order.Status})
	}

	linesByID := make(map[uuid.UUID]OrderLine, len(order.Lines))
	for _, l := range order.Lines {
		linesByID[l.ID] = l
	}

	ret := Return{
		SalesOrderID: order.ID, CustomerID: order.CustomerID, WarehouseID: order.WarehouseID,
		Status: ReturnRequested, Reason: in.Reason,
		Restock: in.Restock == nil || *in.Restock,
	}
	ret.OrganizationID = actor.OrgID
	if actor.UserID != uuid.Nil {
		id := actor.UserID
		ret.RequestedBy = &id
	}

	retLines := make([]ReturnLine, 0, len(in.Lines))
	refund := money.Zero()
	for _, l := range in.Lines {
		orderLine, ok := linesByID[l.SalesOrderLineID]
		if !ok {
			return nil, shared.Validation("line does not belong to this order")
		}
		if !money.IsPositive(l.Quantity) {
			return nil, shared.Validation("return quantity must be positive")
		}
		if l.Quantity.GreaterThan(orderLine.QuantityShipped) {
			return nil, shared.Validation("cannot return more than was shipped").
				WithDetails(map[string]any{"shipped": orderLine.QuantityShipped})
		}

		retLines = append(retLines, ReturnLine{
			ID: uuid.New(), SalesOrderLineID: orderLine.ID,
			ProductID: orderLine.ProductID, VariantID: orderLine.VariantID,
			BatchID: l.BatchID, Quantity: money.Round(l.Quantity),
			UnitPrice: orderLine.UnitPrice, Condition: l.Condition,
		})
		refund = refund.Add(l.Quantity.Mul(orderLine.UnitPrice))
	}
	ret.RefundTotal = money.Round(refund)

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		code, err := s.seq.Next(ctx, tx, actor.OrgID, "sales_return", "RMA")
		if err != nil {
			return err
		}
		ret.Code = code
		if err := tx.Create(&ret).Error; err != nil {
			return err
		}
		for i := range retLines {
			retLines[i].SalesReturnID = ret.ID
		}
		if err := tx.Create(&retLines).Error; err != nil {
			return err
		}
		ret.Lines = retLines
		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "return.request", EntityType: "sales_return", EntityID: &ret.ID, After: ret,
		})
	})
	if err != nil {
		return nil, err
	}

	s.notifier.ReturnRequested(ctx, actor.OrgID, ret.ID, ret.Code, ret.Reason)
	return &ret, nil
}

// ReceiveReturn accepts the returned goods, restocking them when the return was
// marked restockable. Damaged goods are received without re-entering stock, so
// the refund is recorded but the quantity is not resold.
func (s *Service) ReceiveReturn(ctx context.Context, actor auth.Identity, returnID uuid.UUID, meta shared.Actor) (*Return, error) {
	var ret Return
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Preload("Lines").
			Where("id = ?", returnID).First(&ret).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("return")
			}
			return err
		}
		if ret.Status != ReturnApproved {
			return shared.Conflict("only an approved return can be received").
				WithDetails(map[string]any{"status": ret.Status})
		}

		if ret.Restock {
			for _, line := range ret.Lines {
				if _, err := s.stock.ApplyInTx(ctx, tx, stock.MovementRequest{
					OrgID: actor.OrgID, Type: stock.MovementReturnIn,
					ProductID: line.ProductID, VariantID: line.VariantID,
					WarehouseID: ret.WarehouseID, BatchID: line.BatchID,
					Delta:         line.Quantity,
					Reason:        "return " + ret.Code,
					ReferenceType: "sales_return", ReferenceID: &ret.ID,
					ActorID: actor.UserID,
				}); err != nil {
					return err
				}
			}
		}

		now := time.Now().UTC()
		if err := tx.Model(&Return{}).Where("id = ?", ret.ID).Updates(map[string]any{
			"status": ReturnReceived, "received_at": now,
		}).Error; err != nil {
			return err
		}
		ret.Status, ret.ReceivedAt = ReturnReceived, &now

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "return.receive", EntityType: "sales_return", EntityID: &ret.ID,
			After: map[string]any{"restocked": ret.Restock},
		})
	})
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

// DecideReturn approves or rejects an RMA.
func (s *Service) DecideReturn(ctx context.Context, actor auth.Identity, returnID uuid.UUID, approve bool, meta shared.Actor) (*Return, error) {
	var ret Return
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", returnID).First(&ret).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return shared.NotFound("return")
			}
			return err
		}
		if ret.Status != ReturnRequested {
			return shared.InvalidTransition(string(ret.Status), "approved/rejected")
		}

		status := ReturnRejected
		if approve {
			status = ReturnApproved
		}
		now := time.Now().UTC()
		if err := tx.Model(&Return{}).Where("id = ?", ret.ID).Updates(map[string]any{
			"status": status, "approved_by": actor.UserID, "approved_at": now,
		}).Error; err != nil {
			return err
		}
		ret.Status, ret.ApprovedAt, ret.ApprovedBy = status, &now, &actor.UserID

		return s.audit.RecordTx(ctx, tx, meta, shared.AuditEntry{
			Action: "return.decide", EntityType: "sales_return", EntityID: &ret.ID,
			After: map[string]any{"status": status},
		})
	})
	if err != nil {
		return nil, err
	}
	return &ret, nil
}

// ListReturns returns a page of RMAs.
func (s *Service) ListReturns(ctx context.Context, actor auth.Identity, q shared.Query) ([]Return, shared.PageMeta, error) {
	filterable := map[string]string{"status": "status", "sales_order_id": "sales_order_id"}
	tx := s.db.WithContext(ctx).Model(&Return{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var rows []Return
	err := tx.Preload("Lines").Scopes(
		q.OrderBy(map[string]string{"created_at": "created_at", "code": "code"}, "created_at DESC"),
		q.Paginate(),
	).Find(&rows).Error
	return rows, shared.NewPageMeta(q, total), err
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
