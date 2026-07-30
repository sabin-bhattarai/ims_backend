// Package warehouse owns warehouses and their bin/shelf location hierarchy.
package warehouse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

// Warehouse is a physical stock-holding site.
type Warehouse struct {
	shared.OrgScoped
	Code      string     `gorm:"not null" json:"code"`
	Name      string     `gorm:"not null" json:"name"`
	Address   *string    `json:"address,omitempty"`
	City      *string    `json:"city,omitempty"`
	Country   *string    `json:"country,omitempty"`
	ManagerID *uuid.UUID `gorm:"type:uuid" json:"manager_id,omitempty"`
	IsDefault bool       `gorm:"not null;default:false" json:"is_default"`
	IsActive  bool       `gorm:"not null;default:true" json:"is_active"`
}

func (Warehouse) TableName() string { return "warehouses" }

// LocationKind mirrors the location_kind enum.
type LocationKind string

const (
	KindZone  LocationKind = "zone"
	KindAisle LocationKind = "aisle"
	KindRack  LocationKind = "rack"
	KindShelf LocationKind = "shelf"
	KindBin   LocationKind = "bin"
)

// Location is a bin, shelf, rack, aisle or zone within a warehouse.
type Location struct {
	shared.OrgScoped
	WarehouseID uuid.UUID    `gorm:"type:uuid;not null" json:"warehouse_id"`
	ParentID    *uuid.UUID   `gorm:"type:uuid" json:"parent_id,omitempty"`
	Code        string       `gorm:"not null" json:"code"`
	Name        *string      `json:"name,omitempty"`
	Kind        LocationKind `gorm:"type:location_kind;not null;default:bin" json:"kind"`
	Path        *string      `json:"path,omitempty"`
	IsActive    bool         `gorm:"not null;default:true" json:"is_active"`
}

func (Location) TableName() string { return "locations" }

// Service holds warehouse business logic.
type Service struct {
	db    *gorm.DB
	audit *shared.Auditor
	lg    zerolog.Logger
}

// NewService builds the warehouse service.
func NewService(db *gorm.DB, audit *shared.Auditor, lg zerolog.Logger) *Service {
	return &Service{db: db, audit: audit, lg: lg.With().Str("module", "warehouse").Logger()}
}

// Input creates or replaces a warehouse.
type Input struct {
	Code      string     `json:"code"       validate:"required,min=1,max=32"`
	Name      string     `json:"name"       validate:"required,min=1,max=120"`
	Address   *string    `json:"address"    validate:"omitempty,max=500"`
	City      *string    `json:"city"       validate:"omitempty,max=120"`
	Country   *string    `json:"country"    validate:"omitempty,max=120"`
	ManagerID *uuid.UUID `json:"manager_id"`
	IsDefault bool       `json:"is_default"`
	IsActive  *bool      `json:"is_active"`
}

// Create adds a warehouse.
func (s *Service) Create(ctx context.Context, actor auth.Identity, in Input, meta shared.Actor) (*Warehouse, error) {
	w := Warehouse{
		Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		Address: in.Address, City: in.City, Country: in.Country,
		ManagerID: in.ManagerID, IsDefault: in.IsDefault,
		IsActive: in.IsActive == nil || *in.IsActive,
	}
	w.OrganizationID = actor.OrgID

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.clearDefault(tx, actor.OrgID, in.IsDefault); err != nil {
			return err
		}
		// The first warehouse in an organization becomes the default, so stock
		// receipts have somewhere to go without extra setup.
		var count int64
		if err := tx.Model(&Warehouse{}).Scopes(shared.InOrg(actor.OrgID)).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			w.IsDefault = true
		}
		return tx.Create(&w).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a warehouse with this code already exists")
		}
		return nil, fmt.Errorf("create warehouse: %w", err)
	}

	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "warehouse.create", EntityType: "warehouse", EntityID: &w.ID, After: w,
	})
	return &w, nil
}

// Update replaces a warehouse's editable fields.
func (s *Service) Update(ctx context.Context, actor auth.Identity, id uuid.UUID, in Input, meta shared.Actor) (*Warehouse, error) {
	w, err := s.Get(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	before := *w

	w.Code = strings.TrimSpace(in.Code)
	w.Name = strings.TrimSpace(in.Name)
	w.Address, w.City, w.Country, w.ManagerID = in.Address, in.City, in.Country, in.ManagerID
	w.IsDefault = in.IsDefault
	if in.IsActive != nil {
		w.IsActive = *in.IsActive
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.clearDefault(tx, actor.OrgID, in.IsDefault); err != nil {
			return err
		}
		return tx.Save(w).Error
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a warehouse with this code already exists")
		}
		return nil, err
	}

	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "warehouse.update", EntityType: "warehouse", EntityID: &id, Before: before, After: *w,
	})
	return w, nil
}

// Get loads one warehouse.
func (s *Service) Get(ctx context.Context, actor auth.Identity, id uuid.UUID) (*Warehouse, error) {
	var w Warehouse
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).Where("id = ?", id).First(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, shared.NotFound("warehouse")
	}
	return &w, err
}

// List returns a page of warehouses.
func (s *Service) List(ctx context.Context, actor auth.Identity, q shared.Query) ([]Warehouse, shared.PageMeta, error) {
	sortable := map[string]string{"code": "code", "name": "name", "created_at": "created_at"}
	filterable := map[string]string{"is_active": "is_active"}

	tx := s.db.WithContext(ctx).Model(&Warehouse{}).
		Scopes(shared.InOrg(actor.OrgID), q.FilterEq(filterable))
	if q.Search != "" {
		like := "%" + strings.ToLower(q.Search) + "%"
		tx = tx.Where("lower(name) LIKE ? OR lower(code) LIKE ?", like, like)
	}

	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, shared.PageMeta{}, err
	}
	var items []Warehouse
	err := tx.Scopes(q.OrderBy(sortable, "code ASC"), q.Paginate()).Find(&items).Error
	return items, shared.NewPageMeta(q, total), err
}

// Delete soft-deletes a warehouse, refusing while it still holds stock.
func (s *Service) Delete(ctx context.Context, actor auth.Identity, id uuid.UUID, meta shared.Actor) error {
	w, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}

	var onHand int64
	if err := s.db.WithContext(ctx).Table("stock_items").
		Where("warehouse_id = ? AND quantity > 0", id).Count(&onHand).Error; err != nil {
		return err
	}
	if onHand > 0 {
		return shared.Conflict("this warehouse still holds stock; transfer it out first")
	}
	if w.IsDefault {
		return shared.Conflict("the default warehouse cannot be deleted; make another one default first")
	}

	if err := s.db.WithContext(ctx).Delete(w).Error; err != nil {
		return err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "warehouse.delete", EntityType: "warehouse", EntityID: &id, Before: *w,
	})
	return nil
}

// LocationInput creates a location.
type LocationInput struct {
	Code     string       `json:"code"      validate:"required,min=1,max=32"`
	Name     *string      `json:"name"      validate:"omitempty,max=120"`
	Kind     LocationKind `json:"kind"      validate:"required,oneof=zone aisle rack shelf bin"`
	ParentID *uuid.UUID   `json:"parent_id"`
	IsActive *bool        `json:"is_active"`
}

// CreateLocation adds a location to a warehouse, computing its breadcrumb path.
func (s *Service) CreateLocation(ctx context.Context, actor auth.Identity, warehouseID uuid.UUID, in LocationInput, meta shared.Actor) (*Location, error) {
	if _, err := s.Get(ctx, actor, warehouseID); err != nil {
		return nil, err
	}

	loc := Location{
		WarehouseID: warehouseID, Code: strings.TrimSpace(in.Code), Name: in.Name,
		Kind: in.Kind, ParentID: in.ParentID,
		IsActive: in.IsActive == nil || *in.IsActive,
	}
	loc.OrganizationID = actor.OrgID

	path := loc.Code
	if in.ParentID != nil {
		var parent Location
		err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
			Where("id = ? AND warehouse_id = ?", *in.ParentID, warehouseID).First(&parent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.Validation("parent location does not exist in this warehouse")
		}
		if err != nil {
			return nil, err
		}
		if parent.Path != nil {
			path = *parent.Path + "/" + loc.Code
		} else {
			path = parent.Code + "/" + loc.Code
		}
	}
	loc.Path = &path

	if err := s.db.WithContext(ctx).Create(&loc).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, shared.Conflict("a location with this code already exists in this warehouse")
		}
		return nil, err
	}
	s.audit.Record(ctx, meta, shared.AuditEntry{
		Action: "location.create", EntityType: "location", EntityID: &loc.ID, After: loc,
	})
	return &loc, nil
}

// ListLocations returns every location in a warehouse, ordered by path so the
// client can render the hierarchy without sorting.
func (s *Service) ListLocations(ctx context.Context, actor auth.Identity, warehouseID uuid.UUID) ([]Location, error) {
	if _, err := s.Get(ctx, actor, warehouseID); err != nil {
		return nil, err
	}
	var locs []Location
	err := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Where("warehouse_id = ?", warehouseID).
		Order("path ASC NULLS FIRST, code ASC").Find(&locs).Error
	return locs, err
}

// DeleteLocation soft-deletes a location.
func (s *Service) DeleteLocation(ctx context.Context, actor auth.Identity, locationID uuid.UUID) error {
	var onHand int64
	if err := s.db.WithContext(ctx).Table("stock_items").
		Where("location_id = ? AND quantity > 0", locationID).Count(&onHand).Error; err != nil {
		return err
	}
	if onHand > 0 {
		return shared.Conflict("this location still holds stock")
	}
	res := s.db.WithContext(ctx).Scopes(shared.InOrg(actor.OrgID)).
		Delete(&Location{}, "id = ?", locationID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return shared.NotFound("location")
	}
	return nil
}

// clearDefault demotes the current default warehouse when a new one claims it.
func (s *Service) clearDefault(tx *gorm.DB, orgID uuid.UUID, claiming bool) error {
	if !claiming {
		return nil
	}
	return tx.Model(&Warehouse{}).
		Where("organization_id = ? AND is_default", orgID).
		Update("is_default", false).Error
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

// Handler exposes the warehouse HTTP endpoints.
type Handler struct {
	svc   *Service
	actor func(*fiber.Ctx) shared.Actor
}

// NewHandler builds the warehouse handler.
func NewHandler(svc *Service, actor func(*fiber.Ctx) shared.Actor) *Handler {
	return &Handler{svc: svc, actor: actor}
}

// List returns a page of warehouses.
//
//	@Summary	List warehouses
//	@Tags		warehouses
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Warehouse,meta=shared.PageMeta}
//	@Router		/warehouses [get]
func (h *Handler) List(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	items, meta, err := h.svc.List(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, items, meta)
}

// Get returns one warehouse.
//
//	@Summary	Get a warehouse
//	@Tags		warehouses
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Warehouse id"
//	@Success	200	{object}	shared.Envelope{data=Warehouse}
//	@Router		/warehouses/{id} [get]
func (h *Handler) Get(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	whID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	w, err := h.svc.Get(c.UserContext(), id, whID)
	if err != nil {
		return err
	}
	return shared.OK(c, w)
}

// Create adds a warehouse.
//
//	@Summary	Create a warehouse
//	@Tags		warehouses
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		Input	true	"Warehouse"
//	@Success	201		{object}	shared.Envelope{data=Warehouse}
//	@Router		/warehouses [post]
func (h *Handler) Create(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in Input
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	w, err := h.svc.Create(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, w)
}

// Update edits a warehouse.
//
//	@Summary	Update a warehouse
//	@Tags		warehouses
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string	true	"Warehouse id"
//	@Param		payload	body		Input	true	"Warehouse"
//	@Success	200		{object}	shared.Envelope{data=Warehouse}
//	@Router		/warehouses/{id} [put]
func (h *Handler) Update(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	whID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in Input
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	w, err := h.svc.Update(c.UserContext(), id, whID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, w)
}

// Delete soft-deletes a warehouse.
//
//	@Summary	Delete a warehouse
//	@Tags		warehouses
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Warehouse id"
//	@Success	204
//	@Router		/warehouses/{id} [delete]
func (h *Handler) Delete(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	whID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.Delete(c.UserContext(), id, whID, h.actor(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ListLocations returns the locations of a warehouse.
//
//	@Summary	List warehouse locations
//	@Tags		warehouses
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Warehouse id"
//	@Success	200	{object}	shared.Envelope{data=[]Location}
//	@Router		/warehouses/{id}/locations [get]
func (h *Handler) ListLocations(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	whID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	locs, err := h.svc.ListLocations(c.UserContext(), id, whID)
	if err != nil {
		return err
	}
	return shared.OK(c, locs)
}

// CreateLocation adds a location to a warehouse.
//
//	@Summary	Create a warehouse location
//	@Tags		warehouses
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Warehouse id"
//	@Param		payload	body		LocationInput	true	"Location"
//	@Success	201		{object}	shared.Envelope{data=Location}
//	@Router		/warehouses/{id}/locations [post]
func (h *Handler) CreateLocation(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	whID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in LocationInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	loc, err := h.svc.CreateLocation(c.UserContext(), id, whID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, loc)
}

// DeleteLocation removes a location.
//
//	@Summary	Delete a warehouse location
//	@Tags		warehouses
//	@Security	BearerAuth
//	@Param		locationId	path	string	true	"Location id"
//	@Success	204
//	@Router		/warehouses/locations/{locationId} [delete]
func (h *Handler) DeleteLocation(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	locID, err := auth.ParseID(c, "locationId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteLocation(c.UserContext(), id, locID); err != nil {
		return err
	}
	return shared.NoContent(c)
}
