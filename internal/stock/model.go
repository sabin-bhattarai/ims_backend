// Package stock owns on-hand quantities, the append-only movement ledger,
// FIFO cost layers, batches, transfers between warehouses and cycle counts.
package stock

import (
	"time"

	"github.com/google/uuid"

	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// MovementType mirrors the movement_type enum.
type MovementType string

const (
	MovementIn          MovementType = "stock_in"
	MovementOut         MovementType = "stock_out"
	MovementAdjustment  MovementType = "adjustment"
	MovementTransferIn  MovementType = "transfer_in"
	MovementTransferOut MovementType = "transfer_out"
	MovementCount       MovementType = "count"
	MovementReturnIn    MovementType = "return_in"
	MovementReturnOut   MovementType = "return_out"
)

// Item is the current on-hand quantity at one (product, variant, warehouse,
// location, batch) coordinate.
type Item struct {
	ID               uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID   uuid.UUID     `gorm:"type:uuid;not null" json:"organization_id"`
	ProductID        uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID        *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	WarehouseID      uuid.UUID     `gorm:"type:uuid;not null" json:"warehouse_id"`
	LocationID       *uuid.UUID    `gorm:"type:uuid" json:"location_id,omitempty"`
	BatchID          *uuid.UUID    `gorm:"type:uuid" json:"batch_id,omitempty"`
	Quantity         money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity"`
	ReservedQuantity money.Decimal `gorm:"type:numeric(18,4);not null" json:"reserved_quantity"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (Item) TableName() string { return "stock_items" }

// Available is the quantity that may still be promised to a new order.
func (i Item) Available() money.Decimal { return i.Quantity.Sub(i.ReservedQuantity) }

// Movement is one immutable ledger entry.
type Movement struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID  uuid.UUID      `gorm:"type:uuid;not null" json:"organization_id"`
	Type            MovementType   `gorm:"type:movement_type;not null" json:"type"`
	ProductID       uuid.UUID      `gorm:"type:uuid;not null" json:"product_id"`
	VariantID       *uuid.UUID     `gorm:"type:uuid" json:"variant_id,omitempty"`
	WarehouseID     uuid.UUID      `gorm:"type:uuid;not null" json:"warehouse_id"`
	LocationID      *uuid.UUID     `gorm:"type:uuid" json:"location_id,omitempty"`
	BatchID         *uuid.UUID     `gorm:"type:uuid" json:"batch_id,omitempty"`
	QuantityDelta   money.Decimal  `gorm:"type:numeric(18,4);not null" json:"quantity_delta"`
	QuantityBefore  money.Decimal  `gorm:"type:numeric(18,4);not null" json:"quantity_before"`
	QuantityAfter   money.Decimal  `gorm:"type:numeric(18,4);not null" json:"quantity_after"`
	UnitCost        *money.Decimal `gorm:"type:numeric(18,4)" json:"unit_cost,omitempty"`
	Reason          *string        `json:"reason,omitempty"`
	Note            *string        `json:"note,omitempty"`
	ReferenceType   *string        `json:"reference_type,omitempty"`
	ReferenceID     *uuid.UUID     `gorm:"type:uuid" json:"reference_id,omitempty"`
	ActorID         *uuid.UUID     `gorm:"type:uuid" json:"actor_id,omitempty"`
	ClientRequestID *string        `json:"client_request_id,omitempty"`
	OccurredAt      time.Time      `json:"occurred_at"`
	CreatedAt       time.Time      `json:"created_at"`
}

func (Movement) TableName() string { return "stock_movements" }

// CostLayer is a FIFO cost bucket created by a receipt and drawn down by
// issues.
type CostLayer struct {
	ID             uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	OrganizationID uuid.UUID     `gorm:"type:uuid;not null" json:"organization_id"`
	ProductID      uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID      *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	WarehouseID    uuid.UUID     `gorm:"type:uuid;not null" json:"warehouse_id"`
	BatchID        *uuid.UUID    `gorm:"type:uuid" json:"batch_id,omitempty"`
	Quantity       money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity"`
	Remaining      money.Decimal `gorm:"type:numeric(18,4);not null" json:"remaining"`
	UnitCost       money.Decimal `gorm:"type:numeric(18,4);not null" json:"unit_cost"`
	ReceivedAt     time.Time     `json:"received_at"`
	MovementID     *uuid.UUID    `gorm:"type:uuid" json:"movement_id,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
}

func (CostLayer) TableName() string { return "cost_layers" }

// Batch is a lot of a product with an optional expiry date.
type Batch struct {
	shared.OrgScoped
	ProductID      uuid.UUID  `gorm:"type:uuid;not null" json:"product_id"`
	LotNumber      string     `gorm:"not null" json:"lot_number"`
	ExpiryDate     *time.Time `gorm:"type:date" json:"expiry_date,omitempty"`
	ManufacturedAt *time.Time `gorm:"type:date" json:"manufactured_at,omitempty"`
	SupplierID     *uuid.UUID `gorm:"type:uuid" json:"supplier_id,omitempty"`
	ReceivedAt     time.Time  `json:"received_at"`
}

func (Batch) TableName() string { return "batches" }

// TransferStatus mirrors the transfer_status enum.
type TransferStatus string

const (
	TransferDraft     TransferStatus = "draft"
	TransferInTransit TransferStatus = "in_transit"
	TransferReceived  TransferStatus = "received"
	TransferCancelled TransferStatus = "cancelled"
)

// Transfer moves stock between warehouses in two steps (dispatch, receive) so
// goods in transit are never invisible.
type Transfer struct {
	shared.OrgScoped
	Code            string         `gorm:"not null" json:"code"`
	FromWarehouseID uuid.UUID      `gorm:"type:uuid;not null" json:"from_warehouse_id"`
	ToWarehouseID   uuid.UUID      `gorm:"type:uuid;not null" json:"to_warehouse_id"`
	Status          TransferStatus `gorm:"type:transfer_status;not null;default:draft" json:"status"`
	Note            *string        `json:"note,omitempty"`
	CreatedBy       *uuid.UUID     `gorm:"type:uuid" json:"created_by,omitempty"`
	DispatchedBy    *uuid.UUID     `gorm:"type:uuid" json:"dispatched_by,omitempty"`
	DispatchedAt    *time.Time     `json:"dispatched_at,omitempty"`
	ReceivedBy      *uuid.UUID     `gorm:"type:uuid" json:"received_by,omitempty"`
	ReceivedAt      *time.Time     `json:"received_at,omitempty"`
	Lines           []TransferLine `gorm:"foreignKey:TransferID" json:"lines,omitempty"`
}

func (Transfer) TableName() string { return "stock_transfers" }

// TransferLine is one product on a transfer.
type TransferLine struct {
	ID               uuid.UUID     `gorm:"type:uuid;primaryKey" json:"id"`
	TransferID       uuid.UUID     `gorm:"type:uuid;not null" json:"transfer_id"`
	ProductID        uuid.UUID     `gorm:"type:uuid;not null" json:"product_id"`
	VariantID        *uuid.UUID    `gorm:"type:uuid" json:"variant_id,omitempty"`
	BatchID          *uuid.UUID    `gorm:"type:uuid" json:"batch_id,omitempty"`
	Quantity         money.Decimal `gorm:"type:numeric(18,4);not null" json:"quantity"`
	ReceivedQuantity money.Decimal `gorm:"type:numeric(18,4);not null" json:"received_quantity"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

func (TransferLine) TableName() string { return "stock_transfer_lines" }

// CycleCountStatus mirrors the cycle_count_status enum.
type CycleCountStatus string

const (
	CountDraft     CycleCountStatus = "draft"
	CountCounting  CycleCountStatus = "counting"
	CountReview    CycleCountStatus = "review"
	CountCompleted CycleCountStatus = "completed"
	CountCancelled CycleCountStatus = "cancelled"
)

// CycleCount is a physical stock-take.
type CycleCount struct {
	shared.OrgScoped
	Code        string           `gorm:"not null" json:"code"`
	WarehouseID uuid.UUID        `gorm:"type:uuid;not null" json:"warehouse_id"`
	LocationID  *uuid.UUID       `gorm:"type:uuid" json:"location_id,omitempty"`
	Status      CycleCountStatus `gorm:"type:cycle_count_status;not null;default:draft" json:"status"`
	Note        *string          `json:"note,omitempty"`
	CreatedBy   *uuid.UUID       `gorm:"type:uuid" json:"created_by,omitempty"`
	CompletedBy *uuid.UUID       `gorm:"type:uuid" json:"completed_by,omitempty"`
	CompletedAt *time.Time       `json:"completed_at,omitempty"`
	Lines       []CycleCountLine `gorm:"foreignKey:CycleCountID" json:"lines,omitempty"`
}

func (CycleCount) TableName() string { return "cycle_counts" }

// CycleCountLine is one counted coordinate, with the expected quantity frozen
// at sheet-generation time so variance is meaningful.
type CycleCountLine struct {
	ID               uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	CycleCountID     uuid.UUID      `gorm:"type:uuid;not null" json:"cycle_count_id"`
	ProductID        uuid.UUID      `gorm:"type:uuid;not null" json:"product_id"`
	VariantID        *uuid.UUID     `gorm:"type:uuid" json:"variant_id,omitempty"`
	BatchID          *uuid.UUID     `gorm:"type:uuid" json:"batch_id,omitempty"`
	LocationID       *uuid.UUID     `gorm:"type:uuid" json:"location_id,omitempty"`
	ExpectedQuantity money.Decimal  `gorm:"type:numeric(18,4);not null" json:"expected_quantity"`
	CountedQuantity  *money.Decimal `gorm:"type:numeric(18,4)" json:"counted_quantity,omitempty"`
	CountedBy        *uuid.UUID     `gorm:"type:uuid" json:"counted_by,omitempty"`
	CountedAt        *time.Time     `json:"counted_at,omitempty"`
	Note             *string        `json:"note,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

func (CycleCountLine) TableName() string { return "cycle_count_lines" }

// Variance is counted minus expected, or zero while uncounted.
func (l CycleCountLine) Variance() money.Decimal {
	if l.CountedQuantity == nil {
		return money.Zero()
	}
	return l.CountedQuantity.Sub(l.ExpectedQuantity)
}
