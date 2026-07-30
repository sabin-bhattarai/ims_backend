// Package sales owns customers, sales orders and their pick-pack-ship
// progression, invoices and RMA returns.
package sales

import (
	"time"

	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// Customer is a buyer.
type Customer struct {
	shared.OrgScoped
	Code            string        `gorm:"not null" json:"code"`
	Name            string        `gorm:"not null" json:"name"`
	Email           *string       `gorm:"type:citext" json:"email,omitempty"`
	Phone           *string       `json:"phone,omitempty"`
	BillingAddress  *string       `json:"billing_address,omitempty"`
	ShippingAddress *string       `json:"shipping_address,omitempty"`
	TaxNumber       *string       `json:"tax_number,omitempty"`
	CreditLimit     money.Decimal `gorm:"type:numeric(18,4);not null" json:"credit_limit"`
	IsActive        bool          `gorm:"not null;default:true" json:"is_active"`
}

func (Customer) TableName() string { return "customers" }

// OrderStatus mirrors the sales_order_status enum.
type OrderStatus string

const (
	OrderDraft     OrderStatus = "draft"
	OrderConfirmed OrderStatus = "confirmed"
	OrderPicking   OrderStatus = "picking"
	OrderPacked    OrderStatus = "packed"
	OrderShipped   OrderStatus = "shipped"
	OrderDelivered OrderStatus = "delivered"
	OrderCancelled OrderStatus = "cancelled"
)

// soTransitions is the fulfilment state machine. Stock is reserved on confirm
// and actually leaves on ship; the intermediate picking/packed states exist so
// the warehouse floor can see what is in progress.
var soTransitions = map[OrderStatus][]OrderStatus{
	OrderDraft:     {OrderConfirmed, OrderCancelled},
	OrderConfirmed: {OrderPicking, OrderCancelled},
	OrderPicking:   {OrderPacked, OrderCancelled},
	OrderPacked:    {OrderShipped, OrderCancelled},
	OrderShipped:   {OrderDelivered},
	OrderDelivered: {},
	OrderCancelled: {},
}

// CanTransitionTo reports whether a status change is legal.
func (s OrderStatus) CanTransitionTo(next OrderStatus) bool {
	for _, allowed := range soTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Order is a customer order.
type Order struct {
	shared.OrgScoped
	Code            string        `gorm:"not null" json:"code"`
	CustomerID      *uuid.UUID    `gorm:"type:uuid" json:"customer_id,omitempty"`
	WarehouseID     uuid.UUID     `gorm:"type:uuid;not null" json:"warehouse_id"`
	Status          OrderStatus   `gorm:"type:sales_order_status;not null;default:draft" json:"status"`
	Currency        string        `gorm:"type:char(3);not null;default:USD" json:"currency"`
	OrderDate       time.Time     `gorm:"type:date;not null" json:"order_date"`
	RequiredDate    *time.Time    `gorm:"type:date" json:"required_date,omitempty"`
	Subtotal        money.Decimal `gorm:"type:numeric(18,4);not null" json:"subtotal"`
	TaxTotal        money.Decimal `gorm:"type:numeric(18,4);not null" json:"tax_total"`
	DiscountTotal   money.Decimal `gorm:"type:numeric(18,4);not null" json:"discount_total"`
	ShippingTotal   money.Decimal `gorm:"type:numeric(18,4);not null" json:"shipping_total"`
	GrandTotal      money.Decimal `gorm:"type:numeric(18,4);not null" json:"grand_total"`
	ShippingAddress *string       `json:"shipping_address,omitempty"`
	TrackingNumber  *string       `json:"tracking_number,omitempty"`
	Note            *string       `json:"note,omitempty"`
	CreatedBy       *uuid.UUID    `gorm:"type:uuid" json:"created_by,omitempty"`
	ConfirmedAt     *time.Time    `json:"confirmed_at,omitempty"`
	PickedAt        *time.Time    `json:"picked_at,omitempty"`
	PackedAt        *time.Time    `json:"packed_at,omitempty"`
	ShippedAt       *time.Time    `json:"shipped_at,omitempty"`
	DeliveredAt     *time.Time    `json:"delivered_at,omitempty"`
	CancelledAt     *time.Time    `json:"cancelled_at,omitempty"`

	Lines    []OrderLine `gorm:"foreignKey:SalesOrderID" json:"lines,omitempty"`
	Customer *Customer   `gorm:"foreignKey:CustomerID" json:"customer,omitempty"`
}

func (Order) TableName() string { return "sales_orders" }

// OrderLine is one ordered product.
type OrderLine struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	SalesOrderID    uuid.UUID      `gorm:"type:uuid;not null" json:"sales_order_id"`
	ProductID       uuid.UUID      `gorm:"type:uuid;not null" json:"product_id"`
	VariantID       *uuid.UUID     `gorm:"type:uuid" json:"variant_id,omitempty"`
	Description     *string        `json:"description,omitempty"`
	Quantity        money.Decimal  `gorm:"type:numeric(18,4);not null" json:"quantity"`
	QuantityShipped money.Decimal  `gorm:"type:numeric(18,4);not null" json:"quantity_shipped"`
	UnitPrice       money.Decimal  `gorm:"type:numeric(18,4);not null" json:"unit_price"`
	TaxRate         money.Decimal  `gorm:"type:numeric(9,4);not null" json:"tax_rate"`
	DiscountRate    money.Decimal  `gorm:"type:numeric(9,4);not null" json:"discount_rate"`
	LineTotal       money.Decimal  `gorm:"type:numeric(18,4);not null" json:"line_total"`
	COGSTotal       *money.Decimal `gorm:"column:cogs_total;type:numeric(18,4)" json:"cogs_total,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

func (OrderLine) TableName() string { return "sales_order_lines" }

// InvoiceStatus mirrors the invoice_status enum.
type InvoiceStatus string

const (
	InvoiceDraft         InvoiceStatus = "draft"
	InvoiceIssued        InvoiceStatus = "issued"
	InvoicePartiallyPaid InvoiceStatus = "partially_paid"
	InvoicePaid          InvoiceStatus = "paid"
	InvoiceVoid          InvoiceStatus = "void"
)

// Invoice bills a sales order.
type Invoice struct {
	shared.OrgScoped
	Code         string        `gorm:"not null" json:"code"`
	SalesOrderID *uuid.UUID    `gorm:"type:uuid" json:"sales_order_id,omitempty"`
	CustomerID   *uuid.UUID    `gorm:"type:uuid" json:"customer_id,omitempty"`
	Status       InvoiceStatus `gorm:"type:invoice_status;not null;default:draft" json:"status"`
	Currency     string        `gorm:"type:char(3);not null;default:USD" json:"currency"`
	IssuedAt     *time.Time    `json:"issued_at,omitempty"`
	DueAt        *time.Time    `json:"due_at,omitempty"`
	Total        money.Decimal `gorm:"type:numeric(18,4);not null" json:"total"`
	AmountPaid   money.Decimal `gorm:"type:numeric(18,4);not null" json:"amount_paid"`
	Note         *string       `json:"note,omitempty"`
}

func (Invoice) TableName() string { return "invoices" }

// ReturnStatus mirrors the return_status enum.
type ReturnStatus string

const (
	ReturnRequested ReturnStatus = "requested"
	ReturnApproved  ReturnStatus = "approved"
	ReturnRejected  ReturnStatus = "rejected"
	ReturnReceived  ReturnStatus = "received"
	ReturnClosed    ReturnStatus = "closed"
)

// Return is an RMA against a sales order.
type Return struct {
	shared.OrgScoped
	Code         string        `gorm:"not null" json:"code"`
	SalesOrderID uuid.UUID     `gorm:"type:uuid;not null" json:"sales_order_id"`
	CustomerID   *uuid.UUID    `gorm:"type:uuid" json:"customer_id,omitempty"`
	WarehouseID  uuid.UUID     `gorm:"type:uuid;not null" json:"warehouse_id"`
	Status       ReturnStatus  `gorm:"type:return_status;not null;default:requested" json:"status"`
	Reason       string        `gorm:"not null" json:"reason"`
	Restock      bool          `gorm:"not null;default:true" json:"restock"`
	RequestedBy  *uuid.UUID    `gorm:"type:uuid" json:"requested_by,omitempty"`
	ApprovedBy   *uuid.UUID    `gorm:"type:uuid" json:"approved_by,omitempty"`
	ApprovedAt   *time.Time    `json:"approved_at,omitempty"`
	ReceivedAt   *time.Time    `json:"received_at,omitempty"`
	RefundTotal  money.Decimal `gorm:"type:numeric(18,4);not null" json:"refund_total"`

	Lines []ReturnLine `gorm:"foreignKey:SalesReturnID" json:"lines,omitempty"`
}

func (Return) TableName() string { return "sales_returns" }

// ReturnLine is one returned product.
type ReturnLine struct {
	ID               uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	SalesReturnID    uuid.UUID     `gorm:"type:uuid;not null" json:"sales_return_id"`
	SalesOrderLineID uuid.UUID     `gorm:"type:uuid;not null" json:"sales_order_line_id"`
	ProductID        uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID        *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	BatchID          *uuid.UUID    `gorm:"type:uuid" json:"batch_id,omitempty"`
	Quantity         money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity"`
	UnitPrice        money.Decimal `gorm:"type:numeric(18,4);not null" json:"unit_price"`
	Condition        *string       `json:"condition,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (ReturnLine) TableName() string { return "sales_return_lines" }
