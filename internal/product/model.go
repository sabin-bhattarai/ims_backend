// Package product owns the catalog: categories, units of measure, products,
// variants and images.
package product

import (
	"github.com/google/uuid"
	"gorm.io/datatypes"

	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// Category groups products into an arbitrary-depth tree.
type Category struct {
	shared.OrgScoped
	ParentID    *uuid.UUID `gorm:"type:uuid" json:"parent_id,omitempty"`
	Name        string     `gorm:"not null" json:"name"`
	Code        *string    `json:"code,omitempty"`
	Description *string    `json:"description,omitempty"`
}

func (Category) TableName() string { return "categories" }

// Unit is a unit of measure. Precision caps the decimal places accepted for
// quantities, which is how "0.5 units of laptop" gets rejected.
type Unit struct {
	shared.OrgScoped
	Code      string `gorm:"not null" json:"code"`
	Name      string `gorm:"not null" json:"name"`
	Precision int16  `gorm:"not null;default:0" json:"precision"`
}

func (Unit) TableName() string { return "units" }

// Product is a catalog item.
type Product struct {
	shared.OrgScoped
	SKU             string         `gorm:"not null" json:"sku"`
	Name            string         `gorm:"not null" json:"name"`
	Description     *string        `json:"description,omitempty"`
	Barcode         *string        `json:"barcode,omitempty"`
	CategoryID      *uuid.UUID     `gorm:"type:uuid" json:"category_id,omitempty"`
	UnitID          *uuid.UUID     `gorm:"type:uuid" json:"unit_id,omitempty"`
	SupplierID      *uuid.UUID     `gorm:"type:uuid" json:"supplier_id,omitempty"`
	CostPrice       money.Decimal  `gorm:"type:numeric(18,4);not null;default:0" json:"cost_price"`
	SellPrice       money.Decimal  `gorm:"type:numeric(18,4);not null;default:0" json:"sell_price"`
	MinStock        money.Decimal  `gorm:"type:numeric(18,4);not null;default:0" json:"min_stock"`
	MaxStock        *money.Decimal `gorm:"type:numeric(18,4)" json:"max_stock,omitempty"`
	ReorderQuantity money.Decimal  `gorm:"type:numeric(18,4);not null;default:0" json:"reorder_quantity"`
	TrackBatches    bool           `gorm:"not null;default:false" json:"track_batches"`
	ShelfLifeDays   *int           `json:"shelf_life_days,omitempty"`
	IsActive        bool           `gorm:"not null;default:true" json:"is_active"`

	// Preloaded on detail reads only.
	Category *Category `gorm:"foreignKey:CategoryID" json:"category,omitempty"`
	Unit     *Unit     `gorm:"foreignKey:UnitID" json:"unit,omitempty"`
	Variants []Variant `gorm:"foreignKey:ProductID" json:"variants,omitempty"`
	Images   []Image   `gorm:"foreignKey:ProductID" json:"images,omitempty"`
}

func (Product) TableName() string { return "products" }

// Variant is a sellable variation of a product (size, colour, ...).
type Variant struct {
	shared.OrgScoped
	ProductID  uuid.UUID      `gorm:"type:uuid;not null" json:"product_id"`
	SKU        string         `gorm:"not null" json:"sku"`
	Name       string         `gorm:"not null" json:"name"`
	Barcode    *string        `json:"barcode,omitempty"`
	Attributes datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'" json:"attributes"`
	CostPrice  *money.Decimal `gorm:"type:numeric(18,4)" json:"cost_price,omitempty"`
	SellPrice  *money.Decimal `gorm:"type:numeric(18,4)" json:"sell_price,omitempty"`
	MinStock   money.Decimal  `gorm:"type:numeric(18,4);not null;default:0" json:"min_stock"`
	MaxStock   *money.Decimal `gorm:"type:numeric(18,4)" json:"max_stock,omitempty"`
	IsActive   bool           `gorm:"not null;default:true" json:"is_active"`
}

func (Variant) TableName() string { return "product_variants" }

// Image is a product photo.
type Image struct {
	shared.OrgScoped
	ProductID uuid.UUID `gorm:"type:uuid;not null" json:"product_id"`
	URL       string    `gorm:"not null" json:"url"`
	AltText   *string   `json:"alt_text,omitempty"`
	IsPrimary bool      `gorm:"not null;default:false" json:"is_primary"`
	SortOrder int       `gorm:"not null;default:0" json:"sort_order"`
}

func (Image) TableName() string { return "product_images" }

// ScanResult is the response to a barcode lookup: the matched product, the
// matched variant if the barcode was a variant's, and current stock. It is the
// first screen of the warehouse-floor flow, so it carries everything that
// screen needs in one round trip.
type ScanResult struct {
	Product   *Product     `json:"product"`
	Variant   *Variant     `json:"variant,omitempty"`
	MatchedOn string       `json:"matched_on" example:"barcode"`
	Stock     []StockLevel `json:"stock"`
}

// StockLevel is a per-warehouse on-hand summary attached to a scan result.
type StockLevel struct {
	WarehouseID   uuid.UUID     `json:"warehouse_id"`
	WarehouseName string        `json:"warehouse_name"`
	WarehouseCode string        `json:"warehouse_code"`
	Quantity      money.Decimal `json:"quantity"`
	Reserved      money.Decimal `json:"reserved_quantity"`
	Available     money.Decimal `json:"available_quantity"`
}
