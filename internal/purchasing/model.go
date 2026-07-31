// Package purchasing owns suppliers, purchase orders with their approval
// workflow, and goods-received notes supporting partial receipt.
package purchasing

import (
	"time"

	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// Supplier is a vendor.
type Supplier struct {
	shared.OrgScoped
	Code         string  `gorm:"not null" json:"code"`
	Name         string  `gorm:"not null" json:"name"`
	ContactName  *string `json:"contact_name,omitempty"`
	Email        *string `gorm:"type:citext" json:"email,omitempty"`
	Phone        *string `json:"phone,omitempty"`
	Address      *string `json:"address,omitempty"`
	TaxNumber    *string `json:"tax_number,omitempty"`
	PaymentTerms *string `json:"payment_terms,omitempty"`
	LeadTimeDays int     `gorm:"not null;default:0" json:"lead_time_days"`
	IsActive     bool    `gorm:"not null;default:true" json:"is_active"`
	Notes        *string `json:"notes,omitempty"`
}

func (Supplier) TableName() string { return "suppliers" }

// Status mirrors the purchase_order_status enum.
type Status string

const (
	StatusDraft             Status = "draft"
	StatusPendingApproval   Status = "pending_approval"
	StatusApproved          Status = "approved"
	StatusRejected          Status = "rejected"
	StatusPartiallyReceived Status = "partially_received"
	StatusReceived          Status = "received"
	StatusCancelled         Status = "cancelled"
)

// canTransitionTo encodes the PO state machine. Keeping it as data rather than
// scattered if-statements is what makes the workflow auditable.
var poTransitions = map[Status][]Status{
	StatusDraft:             {StatusPendingApproval, StatusCancelled},
	StatusPendingApproval:   {StatusApproved, StatusRejected, StatusCancelled},
	StatusApproved:          {StatusPartiallyReceived, StatusReceived, StatusCancelled},
	StatusRejected:          {StatusDraft, StatusCancelled},
	StatusPartiallyReceived: {StatusPartiallyReceived, StatusReceived, StatusCancelled},
	StatusReceived:          {},
	StatusCancelled:         {},
}

// CanTransitionTo reports whether a status change is legal.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range poTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// PurchaseOrder is an order placed with a supplier.
type PurchaseOrder struct {
	shared.OrgScoped
	Code            string        `gorm:"not null" json:"code"`
	SupplierID      uuid.UUID     `gorm:"type:uuid;not null" json:"supplier_id"`
	WarehouseID     uuid.UUID     `gorm:"type:uuid;not null" json:"warehouse_id"`
	Status          Status        `gorm:"type:purchase_order_status;not null;default:draft" json:"status"`
	Currency        string        `gorm:"type:char(3);not null;default:USD" json:"currency"`
	ExpectedDate    *time.Time    `gorm:"type:date" json:"expected_date,omitempty"`
	Subtotal        money.Decimal `gorm:"type:numeric(18,4);not null" json:"subtotal"`
	TaxTotal        money.Decimal `gorm:"type:numeric(18,4);not null" json:"tax_total"`
	DiscountTotal   money.Decimal `gorm:"type:numeric(18,4);not null" json:"discount_total"`
	ShippingTotal   money.Decimal `gorm:"type:numeric(18,4);not null" json:"shipping_total"`
	GrandTotal      money.Decimal `gorm:"type:numeric(18,4);not null" json:"grand_total"`
	Note            *string       `json:"note,omitempty"`
	CreatedBy       *uuid.UUID    `gorm:"type:uuid" json:"created_by,omitempty"`
	SubmittedAt     *time.Time    `json:"submitted_at,omitempty"`
	ApprovedBy      *uuid.UUID    `gorm:"type:uuid" json:"approved_by,omitempty"`
	ApprovedAt      *time.Time    `json:"approved_at,omitempty"`
	RejectedBy      *uuid.UUID    `gorm:"type:uuid" json:"rejected_by,omitempty"`
	RejectedAt      *time.Time    `json:"rejected_at,omitempty"`
	RejectionReason *string       `json:"rejection_reason,omitempty"`

	Lines    []PurchaseOrderLine `gorm:"foreignKey:PurchaseOrderID" json:"lines,omitempty"`
	Supplier *Supplier           `gorm:"foreignKey:SupplierID" json:"supplier,omitempty"`
}

func (PurchaseOrder) TableName() string { return "purchase_orders" }

// PurchaseOrderLine is one ordered product.
type PurchaseOrderLine struct {
	ID               uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	PurchaseOrderID  uuid.UUID     `gorm:"type:uuid;not null" json:"purchase_order_id"`
	ProductID        uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID        *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	Description      *string       `json:"description,omitempty"`
	QuantityOrdered  money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity_ordered"`
	QuantityReceived money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity_received"`
	UnitPrice        money.Decimal `gorm:"type:numeric(18,4);not null" json:"unit_price"`
	TaxRate          money.Decimal `gorm:"type:numeric(9,4);not null" json:"tax_rate"`
	DiscountRate     money.Decimal `gorm:"type:numeric(9,4);not null" json:"discount_rate"`
	LineTotal        money.Decimal `gorm:"type:numeric(18,4);not null" json:"line_total"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (PurchaseOrderLine) TableName() string { return "purchase_order_lines" }

// Outstanding is the quantity still to be received on this line.
func (l PurchaseOrderLine) Outstanding() money.Decimal {
	return l.QuantityOrdered.Sub(l.QuantityReceived)
}

// GoodsReceipt records a delivery against a purchase order.
type GoodsReceipt struct {
	shared.OrgScoped
	Code            string     `gorm:"not null" json:"code"`
	PurchaseOrderID uuid.UUID  `gorm:"type:uuid;not null" json:"purchase_order_id"`
	WarehouseID     uuid.UUID  `gorm:"type:uuid;not null" json:"warehouse_id"`
	SupplierRef     *string    `json:"supplier_ref,omitempty"`
	Note            *string    `json:"note,omitempty"`
	ReceivedBy      *uuid.UUID `gorm:"type:uuid" json:"received_by,omitempty"`
	ReceivedAt      time.Time  `json:"received_at"`

	Lines []GoodsReceiptLine `gorm:"foreignKey:GoodsReceiptID" json:"lines,omitempty"`
}

func (GoodsReceipt) TableName() string { return "goods_receipts" }

// GoodsReceiptLine is one received quantity, with the cost that seeds its FIFO
// layer.
type GoodsReceiptLine struct {
	ID                  uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	GoodsReceiptID      uuid.UUID     `gorm:"type:uuid;not null" json:"goods_receipt_id"`
	PurchaseOrderLineID uuid.UUID     `gorm:"type:uuid;not null" json:"purchase_order_line_id"`
	ProductID           uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID           *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	BatchID             *uuid.UUID    `gorm:"type:uuid" json:"batch_id,omitempty"`
	LocationID          *uuid.UUID    `gorm:"type:uuid" json:"location_id,omitempty"`
	Quantity            money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity"`
	UnitCost            money.Decimal `gorm:"type:numeric(18,4);not null" json:"unit_cost"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}

func (GoodsReceiptLine) TableName() string { return "goods_receipt_lines" }
