package product

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// Service holds catalog business logic.
type Service struct {
	db    *gorm.DB
	cache *platform.Cache
	audit *shared.Auditor
	lg    zerolog.Logger
}

// NewService builds the catalog service.
func NewService(db *gorm.DB, cache *platform.Cache, audit *shared.Auditor, lg zerolog.Logger) *Service {
	return &Service{db: db, cache: cache, audit: audit, lg: lg.With().Str("module", "product").Logger()}
}

var productSortable = map[string]string{
	"created_at": "products.created_at",
	"name":       "products.name",
	"sku":        "products.sku",
	"cost_price": "products.cost_price",
	"sell_price": "products.sell_price",
}

var productFilterable = map[string]string{
	"category_id": "products.category_id",
	"supplier_id": "products.supplier_id",
	"unit_id":     "products.unit_id",
	"is_active":   "products.is_active",
}

// CreateProductInput creates a catalog item.
type CreateProductInput struct {
	SKU             string         `json:"sku"              validate:"required,min=1,max=64"`
	Name            string         `json:"name"             validate:"required,min=1,max=255"`
	Description     *string        `json:"description"      validate:"omitempty,max=5000"`
	Barcode         *string        `json:"barcode"          validate:"omitempty,max=64"`
	CategoryID      *uuid.UUID     `json:"category_id"`
	UnitID          *uuid.UUID     `json:"unit_id"`
	SupplierID      *uuid.UUID     `json:"supplier_id"`
	CostPrice       money.Decimal  `json:"cost_price"`
	SellPrice       money.Decimal  `json:"sell_price"`
	MinStock        money.Decimal  `json:"min_stock"`
	MaxStock        *money.Decimal `json:"max_stock"`
	ReorderQuantity money.Decimal  `json:"reorder_quantity"`
	TrackBatches    bool           `json:"track_batches"`
	ShelfLifeDays   *int           `json:"shelf_life_days"  validate:"omitempty,gt=0"`
	IsActive        *bool          `json:"is_active"`
}

// CreateProduct adds a product to the catalog.
func (s *Service) CreateProduct(ctx context.Context, actor auth.Identity, in CreateProductInput, meta shared.Actor) (*Product, error) {
	if err := s.validatePricing(in.CostPrice, in.SellPrice, in.MinStock, in.MaxStock); err != nil {
		return nil, err
	}

	p := Product{
		SKU:             strings.TrimSpace(in.SKU),
		Name:            strings.TrimSpace(in.Name),
		Description:     in.Description,
		Barcode:         normalizeBarcode(in.Barcode),
		CategoryID:      in.CategoryID,
		UnitID:          in.UnitID,
		SupplierID:      in.SupplierID,
		CostPrice:       money.Round(in.CostPrice),
		SellPrice:       money.Round(in.SellPrice),
		MinStock:        money.Round(in.MinStock),
		ReorderQuantity: money.Round(in.ReorderQuantity),
		TrackBatches:    in.TrackBatches,
		ShelfLifeDays:   in.ShelfLifeDays,
		IsActive:        in.IsActive == nil || *in.IsActive,
	}
	p.OrganizationID = actor.OrgID
	if in.MaxStock != nil {
		rounded := money.Round(*in.MaxStock)
		p.MaxStock = &rounded
	}

	if err := s.assertReferencesExist(ctx, actor.OrgID, in.CategoryID, in.UnitID, in.SupplierID); err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Create(&p).Error; err != nil {
		return nil, s.translateConflict(err, p.SKU)
	}

	s.invalidate(ctx, actor.OrgID)
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "product.create", EntityType: "product", EntityID: &p.ID, After: p,
	})
	return &p, nil
}

// UpdateProductInput edits a product; nil fields are left unchanged.
type UpdateProductInput struct {
	SKU             *string        `json:"sku"              validate:"omitempty,min=1,max=64"`
	Name            *string        `json:"name"             validate:"omitempty,min=1,max=255"`
	Description     *string        `json:"description"      validate:"omitempty,max=5000"`
	Barcode         *string        `json:"barcode"          validate:"omitempty,max=64"`
	CategoryID      *uuid.UUID     `json:"category_id"`
	UnitID          *uuid.UUID     `json:"unit_id"`
	SupplierID      *uuid.UUID     `json:"supplier_id"`
	CostPrice       *money.Decimal `json:"cost_price"`
	SellPrice       *money.Decimal `json:"sell_price"`
	MinStock        *money.Decimal `json:"min_stock"`
	MaxStock        *money.Decimal `json:"max_stock"`
	ReorderQuantity *money.Decimal `json:"reorder_quantity"`
	TrackBatches    *bool          `json:"track_batches"`
	ShelfLifeDays   *int           `json:"shelf_life_days"  validate:"omitempty,gt=0"`
	IsActive        *bool          `json:"is_active"`
}

// UpdateProduct applies a partial update.
func (s *Service) UpdateProduct(ctx context.Context, actor auth.Identity, id uuid.UUID, in UpdateProductInput, meta shared.Actor) (*Product, error) {
	existing, err := s.GetProduct(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	before := *existing

	updates := map[string]any{}
	if in.SKU != nil {
		updates["sku"] = strings.TrimSpace(*in.SKU)
	}
	if in.Name != nil {
		updates["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if in.Barcode != nil {
		updates["barcode"] = normalizeBarcode(in.Barcode)
	}
	if in.CategoryID != nil {
		updates["category_id"] = *in.CategoryID
	}
	if in.UnitID != nil {
		updates["unit_id"] = *in.UnitID
	}
	if in.SupplierID != nil {
		updates["supplier_id"] = *in.SupplierID
	}
	if in.CostPrice != nil {
		updates["cost_price"] = money.Round(*in.CostPrice)
	}
	if in.SellPrice != nil {
		updates["sell_price"] = money.Round(*in.SellPrice)
	}
	if in.MinStock != nil {
		updates["min_stock"] = money.Round(*in.MinStock)
	}
	if in.MaxStock != nil {
		updates["max_stock"] = money.Round(*in.MaxStock)
	}
	if in.ReorderQuantity != nil {
		updates["reorder_quantity"] = money.Round(*in.ReorderQuantity)
	}
	if in.ShelfLifeDays != nil {
		updates["shelf_life_days"] = *in.ShelfLifeDays
	}
	if in.IsActive != nil {
		updates["is_active"] = *in.IsActive
	}
	if in.TrackBatches != nil && *in.TrackBatches != existing.TrackBatches {
		// Flipping batch tracking on a product that already holds stock would
		// leave existing quantities unattributable to any batch.
		if err := s.assertNoStock(ctx, actor.OrgID, existing.ID); err != nil {
			return nil, err
		}
		updates["track_batches"] = *in.TrackBatches
	}
	if len(updates) == 0 {
		return existing, nil
	}

	if err := s.assertReferencesExist(ctx, actor.OrgID, in.CategoryID, in.UnitID, in.SupplierID); err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Model(&Product{}).
		Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return nil, s.translateConflict(err, existing.SKU)
	}

	updated, err := s.GetProduct(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	s.invalidate(ctx, actor.OrgID)
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "product.update", EntityType: "product", EntityID: &id,
		Before: before, After: *updated,
	})
	return updated, nil
}

// GetProduct loads a product with its category, unit, variants and images.
func (s *Service) GetProduct(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Product, error) {
	var p Product
	err := s.db.WithContext(ctx).
		Scopes(shared.InOrg(actor.OrgID)).
		Preload("Category").Preload("Unit").
		Preload("Variants", func(db *gorm.DB) *gorm.DB { return db.Order("sku ASC") }).
		Preload("Images", func(db *gorm.DB) *gorm.DB { return db.Order("sort_order ASC") }).
		Where("id = ?", id).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("product")
	}
	return &p, err
}

// ListProducts returns a page of products.
func (s *Service) ListProducts(ctx context.Context, actor auth.Identity, q shared.Query) ([]Product, shared.PageMeta, error) {
	tx := s.db.WithContext(ctx).Model(&Product{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(productFilterable))

	if q.Search != "" {
		// SKU and barcode match exactly-ish; name uses the trigram index.
		like := "%" + strings.ToLower(q.Search) + "%"
		tx = tx.Where(
			"lower(products.name) LIKE ? OR lower(products.sku) LIKE ? OR products.barcode = ?",
			like, like, q.Search,
		)
	}
	if v := q.Filters["below_min_stock"]; v == "true" {
		// Products whose total on-hand across warehouses sits at or below their
		// reorder threshold.
		tx = tx.Where(`products.min_stock > 0 AND COALESCE((
			SELECT SUM(si.quantity) FROM stock_items si WHERE si.product_id = products.id
		), 0) <= products.min_stock`)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}

	var items []Product
	err := tx.Preload("Category").Preload("Unit").
		Scopes(q.OrderBy(productSortable, "products.created_at DESC"), q.Paginate()).
		Find(&items).Error
	return items, shared.NewPageMeta(q, total), err
}

// DeleteProduct soft-deletes a product. Products holding stock are refused:
// deleting them would silently orphan on-hand quantity.
func (s *Service) DeleteProduct(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) error {
	existing, err := s.GetProduct(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.assertNoStock(ctx, actor.OrgID, id); err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Delete(&Product{}, "id = ?", id).Error; err != nil {
		return fmt.Errorf("delete product: %w", err)
	}
	s.invalidate(ctx, actor.OrgID)
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "product.delete", EntityType: "product", EntityID: &id, Before: *existing,
	})
	return nil
}

// Scan resolves a scanned barcode (or an exact SKU) to a product, its variant
// and its current stock. This is the mobile app's entry point, so it is cached
// briefly and answers in one round trip.
func (s *Service) Scan(ctx context.Context, actor auth.Identity, code string) (*ScanResult, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, shared.Validation("a barcode or SKU is required")
	}

	// Variant barcodes are checked first: a variant barcode is more specific
	// than its parent product's.
	var variant Variant
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("barcode = ? OR lower(sku) = lower(?)", code, code).First(&variant).Error
	if err == nil {
		parent, err := s.GetProduct(ctx, actor, variant.ProductID)
		if err != nil {
			return nil, err
		}
		levels, err := s.stockLevels(ctx, actor.OrgID, parent.ID, &variant.ID)
		if err != nil {
			return nil, err
		}
		return &ScanResult{Product: parent, Variant: &variant, MatchedOn: "variant", Stock: levels}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var p Product
	err = s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("barcode = ? OR lower(sku) = lower(?)", code, code).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("no product matches that code")
	}
	if err != nil {
		return nil, err
	}

	full, err := s.GetProduct(ctx, actor, p.ID)
	if err != nil {
		return nil, err
	}
	levels, err := s.stockLevels(ctx, actor.OrgID, p.ID, nil)
	if err != nil {
		return nil, err
	}
	matched := "sku"
	if p.Barcode != nil && *p.Barcode == code {
		matched = "barcode"
	}
	return &ScanResult{Product: full, MatchedOn: matched, Stock: levels}, nil
}

// stockLevels aggregates on-hand quantity per warehouse for a product.
func (s *Service) stockLevels(ctx context.Context, orgID, productID uuid.UUID, variantID *uuid.UUID) ([]StockLevel, error) {
	tx := s.db.WithContext(ctx).
		Table("stock_items si").
		Select(`w.id AS warehouse_id, w.name AS warehouse_name, w.code AS warehouse_code,
		        COALESCE(SUM(si.quantity), 0) AS quantity,
		        COALESCE(SUM(si.reserved_quantity), 0) AS reserved,
		        COALESCE(SUM(si.quantity - si.reserved_quantity), 0) AS available`).
		Joins("JOIN warehouses w ON w.id = si.warehouse_id AND w.deleted_at IS NULL").
		Where("si.organization_id = ? AND si.product_id = ?", orgID, productID).
		Group("w.id, w.name, w.code").
		Order("w.code ASC")

	if variantID != nil {
		tx = tx.Where("si.variant_id = ?", *variantID)
	}

	var levels []StockLevel
	err := tx.Scan(&levels).Error
	return levels, err
}

// --- categories, units, variants, images ----------------------------------

// CategoryInput creates or replaces a category.
type CategoryInput struct {
	Name        string     `json:"name"        validate:"required,min=1,max=120"`
	Code        *string    `json:"code"        validate:"omitempty,max=32"`
	Description *string    `json:"description" validate:"omitempty,max=2000"`
	ParentID    *uuid.UUID `json:"parent_id"`
}

// CreateCategory adds a category.
func (s *Service) CreateCategory(ctx context.Context, actor auth.Identity, in CategoryInput, meta shared.Actor) (*Category, error) {
	cat := Category{Name: strings.TrimSpace(in.Name), Code: in.Code, Description: in.Description, ParentID: in.ParentID}
	cat.OrganizationID = actor.OrgID

	if in.ParentID != nil {
		if err := s.exists(ctx, &Category{}, actor.OrgID, *in.ParentID); err != nil {
			return nil, shared.Validation("parent category does not exist")
		}
	}
	if err := s.db.WithContext(ctx).Create(&cat).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a category with this name already exists")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "category.create", EntityType: "category", EntityID: &cat.ID, After: cat,
	})
	return &cat, nil
}

// ListCategories returns every category in the organization. The list is small
// and drives pickers, so it is returned whole rather than paginated.
func (s *Service) ListCategories(ctx context.Context, actor auth.Identity) ([]Category, error) {
	var cats []Category
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Order("name ASC").Find(&cats).Error
	return cats, err
}

// UpdateCategory renames or re-parents a category.
func (s *Service) UpdateCategory(ctx context.Context, actor auth.Identity, id uuid.UUID, in CategoryInput, meta shared.Actor) (*Category, error) {
	var cat Category
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", id).First(&cat).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("category")
	}
	if err != nil {
		return nil, err
	}
	if in.ParentID != nil && *in.ParentID == id {
		return nil, shared.Validation("a category cannot be its own parent")
	}
	before := cat

	cat.Name = strings.TrimSpace(in.Name)
	cat.Code = in.Code
	cat.Description = in.Description
	cat.ParentID = in.ParentID
	if err := s.db.WithContext(ctx).Save(&cat).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a category with this name already exists")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "category.update", EntityType: "category", EntityID: &id, Before: before, After: cat,
	})
	return &cat, nil
}

// DeleteCategory soft-deletes a category, leaving its products uncategorised.
func (s *Service) DeleteCategory(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) error {
	res := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Delete(&Category{}, "id = ?", id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return shared.NotFound("category")
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "category.delete", EntityType: "category", EntityID: &id,
	})
	return nil
}

// UnitInput creates or replaces a unit of measure.
type UnitInput struct {
	Code      string `json:"code"      validate:"required,min=1,max=16"`
	Name      string `json:"name"      validate:"required,min=1,max=64"`
	Precision int16  `json:"precision" validate:"gte=0,lte=4"`
}

// CreateUnit adds a unit of measure.
func (s *Service) CreateUnit(ctx context.Context, actor auth.Identity, in UnitInput, meta shared.Actor) (*Unit, error) {
	u := Unit{Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name), Precision: in.Precision}
	u.OrganizationID = actor.OrgID
	if err := s.db.WithContext(ctx).Create(&u).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a unit with this code already exists")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "unit.create", EntityType: "unit", EntityID: &u.ID, After: u,
	})
	return &u, nil
}

// ListUnits returns every unit of measure.
func (s *Service) ListUnits(ctx context.Context, actor auth.Identity) ([]Unit, error) {
	var units []Unit
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Order("code ASC").Find(&units).Error
	return units, err
}

// VariantInput creates a product variant.
type VariantInput struct {
	SKU        string         `json:"sku"        validate:"required,min=1,max=64"`
	Name       string         `json:"name"       validate:"required,min=1,max=255"`
	Barcode    *string        `json:"barcode"    validate:"omitempty,max=64"`
	Attributes map[string]any `json:"attributes"`
	CostPrice  *money.Decimal `json:"cost_price"`
	SellPrice  *money.Decimal `json:"sell_price"`
	MinStock   money.Decimal  `json:"min_stock"`
	MaxStock   *money.Decimal `json:"max_stock"`
	IsActive   *bool          `json:"is_active"`
}

// CreateVariant adds a variant to a product.
func (s *Service) CreateVariant(ctx context.Context, actor auth.Identity, productID uuid.UUID, in VariantInput, meta shared.Actor) (*Variant, error) {
	if _, err := s.GetProduct(ctx, actor, productID); err != nil {
		return nil, err
	}

	v := Variant{
		ProductID: productID,
		SKU:       strings.TrimSpace(in.SKU),
		Name:      strings.TrimSpace(in.Name),
		Barcode:   normalizeBarcode(in.Barcode),
		CostPrice: in.CostPrice,
		SellPrice: in.SellPrice,
		MinStock:  money.Round(in.MinStock),
		MaxStock:  in.MaxStock,
		IsActive:  in.IsActive == nil || *in.IsActive,
	}
	v.OrganizationID = actor.OrgID
	if in.Attributes != nil {
		v.Attributes = shared.JSONB(in.Attributes)
	} else {
		v.Attributes = shared.JSONB(map[string]any{})
	}

	if err := s.db.WithContext(ctx).Create(&v).Error; err != nil {
		return nil, s.translateConflict(err, v.SKU)
	}
	s.invalidate(ctx, actor.OrgID)
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "variant.create", EntityType: "product_variant", EntityID: &v.ID, After: v,
	})
	return &v, nil
}

// AddImageInput attaches an image to a product.
type AddImageInput struct {
	URL       string  `json:"url"        validate:"required,url,max=2000"`
	AltText   *string `json:"alt_text"   validate:"omitempty,max=255"`
	IsPrimary bool    `json:"is_primary"`
	SortOrder int     `json:"sort_order"`
}

// AddImage records a product image URL. Uploading the binary itself is the
// client's job (direct-to-storage); the API only stores the reference.
func (s *Service) AddImage(ctx context.Context, actor auth.Identity, productID uuid.UUID, in AddImageInput, meta shared.Actor) (*Image, error) {
	if _, err := s.GetProduct(ctx, actor, productID); err != nil {
		return nil, err
	}

	img := Image{
		ProductID: productID, URL: in.URL, AltText: in.AltText,
		IsPrimary: in.IsPrimary, SortOrder: in.SortOrder,
	}
	img.OrganizationID = actor.OrgID

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.IsPrimary {
			// A partial unique index enforces one primary image per product, so
			// demote the incumbent first.
			if err := tx.Model(&Image{}).
				Where("product_id = ? AND is_primary", productID).
				Update("is_primary", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&img).Error
	})
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "product.image_add", EntityType: "product", EntityID: &productID, After: img,
	})
	return &img, nil
}

// DeleteImage removes a product image.
func (s *Service) DeleteImage(ctx context.Context, actor auth.Identity, imageID uuid.UUID) error {
	res := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Delete(&Image{}, "id = ?", imageID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return shared.NotFound("image")
	}
	return nil
}

// --- internals -------------------------------------------------------------

func (s *Service) validatePricing(cost, sell, minStock money.Decimal, maxStock *money.Decimal) error {
	if money.IsNegative(cost) || money.IsNegative(sell) {
		return shared.Validation("prices cannot be negative")
	}
	if money.IsNegative(minStock) {
		return shared.Validation("min_stock cannot be negative")
	}
	if maxStock != nil && maxStock.LessThan(minStock) {
		return shared.Validation("max_stock cannot be lower than min_stock")
	}
	return nil
}

func (s *Service) assertReferencesExist(ctx context.Context, orgID uuid.UUID, categoryID, unitID, supplierID *uuid.UUID) error {
	if categoryID != nil {
		if err := s.exists(ctx, &Category{}, orgID, *categoryID); err != nil {
			return shared.Validation("category does not exist")
		}
	}
	if unitID != nil {
		if err := s.exists(ctx, &Unit{}, orgID, *unitID); err != nil {
			return shared.Validation("unit does not exist")
		}
	}
	if supplierID != nil {
		var count int64
		if err := s.db.WithContext(ctx).Table("suppliers").
			Where("id = ? AND organization_id = ? AND deleted_at IS NULL", *supplierID, orgID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return shared.Validation("supplier does not exist")
		}
	}
	return nil
}

func (s *Service) exists(ctx context.Context, model any, orgID, id uuid.UUID) error {
	var count int64
	err := s.db.WithContext(ctx).Model(model).
		Scopes(shared.InOrg(orgID)).Where("id = ?", id).Count(&count).Error
	if err != nil {
		return err
	}
	if count == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// assertNoStock refuses an operation while on-hand quantity exists.
func (s *Service) assertNoStock(ctx context.Context, orgID, productID uuid.UUID) error {
	var onHand money.Decimal
	err := s.db.WithContext(ctx).Table("stock_items").
		Select("COALESCE(SUM(quantity), 0)").
		Where("organization_id = ? AND product_id = ?", orgID, productID).
		Scan(&onHand).Error
	if err != nil {
		return err
	}
	if money.IsPositive(onHand) {
		return shared.Conflict("this product still holds stock").
			WithDetails(map[string]any{"on_hand": onHand})
	}
	return nil
}

func (s *Service) translateConflict(err error, sku string) error {
	if !isUniqueViolation(err) {
		return fmt.Errorf("persist product: %w", err)
	}
	switch {
	case strings.Contains(err.Error(), "barcode"):
		return shared.Conflict("another item already uses this barcode")
	default:
		return shared.Conflict("SKU " + sku + " already exists")
	}
}

// invalidate drops cached catalog reads for an organization.
func (s *Service) invalidate(ctx context.Context, orgID uuid.UUID) {
	s.cache.InvalidatePrefix(ctx, s.cache.Key("catalog", orgID.String()))
}

func normalizeBarcode(b *string) *string {
	if b == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*b)
	if trimmed == "" {
		// An empty string would collide with other blank barcodes under the
		// unique index; NULL is the correct "no barcode".
		return nil
	}
	return &trimmed
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
