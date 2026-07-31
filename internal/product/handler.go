package product

import (
	"github.com/gofiber/fiber/v2"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Handler exposes the catalog HTTP endpoints.
type Handler struct {
	svc   *Service
	actor func(*fiber.Ctx) shared.Actor
}

// NewHandler builds the catalog handler. actor is injected to avoid importing
// the middleware package.
func NewHandler(svc *Service, actor func(*fiber.Ctx) shared.Actor) *Handler {
	return &Handler{svc: svc, actor: actor}
}

// List returns a page of products.
//
//	@Summary	List products
//	@Tags		products
//	@Produce	json
//	@Security	BearerAuth
//	@Param		page				query		int		false	"Page number"
//	@Param		per_page			query		int		false	"Items per page"
//	@Param		q					query		string	false	"Search name, SKU or barcode"
//	@Param		sort				query		string	false	"Sort, e.g. -created_at"
//	@Param		filter[category_id]	query		string	false	"Filter by category"
//	@Param		filter[is_active]	query		bool	false	"Filter by active flag"
//	@Param		filter[below_min_stock]	query	bool	false	"Only products at or below their minimum"
//	@Success	200					{object}	shared.Envelope{data=[]Product,meta=shared.PageMeta}
//	@Router		/products [get]
func (h *Handler) List(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	items, meta, err := h.svc.ListProducts(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, items, meta)
}

// Get returns one product.
//
//	@Summary	Get a product
//	@Tags		products
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Product id"
//	@Success	200	{object}	shared.Envelope{data=Product}
//	@Failure	404	{object}	shared.ErrorEnvelope
//	@Router		/products/{id} [get]
func (h *Handler) Get(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	productID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	p, err := h.svc.GetProduct(c.UserContext(), id, productID)
	if err != nil {
		return err
	}
	return shared.OK(c, p)
}

// Create adds a product.
//
//	@Summary	Create a product
//	@Tags		products
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		CreateProductInput	true	"Product"
//	@Success	201		{object}	shared.Envelope{data=Product}
//	@Failure	409		{object}	shared.ErrorEnvelope
//	@Router		/products [post]
func (h *Handler) Create(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in CreateProductInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	p, err := h.svc.CreateProduct(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, p)
}

// Update edits a product.
//
//	@Summary	Update a product
//	@Tags		products
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string				true	"Product id"
//	@Param		payload	body		UpdateProductInput	true	"Fields to change"
//	@Success	200		{object}	shared.Envelope{data=Product}
//	@Router		/products/{id} [patch]
func (h *Handler) Update(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	productID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in UpdateProductInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	p, err := h.svc.UpdateProduct(c.UserContext(), id, productID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, p)
}

// Delete soft-deletes a product.
//
//	@Summary	Delete a product
//	@Tags		products
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Product id"
//	@Success	204
//	@Failure	409	{object}	shared.ErrorEnvelope
//	@Router		/products/{id} [delete]
func (h *Handler) Delete(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	productID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteProduct(c.UserContext(), id, productID, h.actor(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// Scan resolves a barcode or SKU to a product plus live stock levels.
//
//	@Summary		Scan a barcode
//	@Description	Primary mobile entry point: resolves a scanned code to a product, variant and per-warehouse stock in one call.
//	@Tags			products
//	@Produce		json
//	@Security		BearerAuth
//	@Param			code	path		string	true	"Barcode or SKU"
//	@Success		200		{object}	shared.Envelope{data=ScanResult}
//	@Failure		404		{object}	shared.ErrorEnvelope
//	@Router			/products/scan/{code} [get]
func (h *Handler) Scan(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	result, err := h.svc.Scan(c.UserContext(), id, c.Params("code"))
	if err != nil {
		return err
	}
	return shared.OK(c, result)
}

// CreateVariant adds a variant.
//
//	@Summary	Create a product variant
//	@Tags		products
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Product id"
//	@Param		payload	body		VariantInput	true	"Variant"
//	@Success	201		{object}	shared.Envelope{data=Variant}
//	@Router		/products/{id}/variants [post]
func (h *Handler) CreateVariant(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	productID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in VariantInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	v, err := h.svc.CreateVariant(c.UserContext(), id, productID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, v)
}

// AddImage attaches an image URL to a product.
//
//	@Summary	Add a product image
//	@Tags		products
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Product id"
//	@Param		payload	body		AddImageInput	true	"Image"
//	@Success	201		{object}	shared.Envelope{data=Image}
//	@Router		/products/{id}/images [post]
func (h *Handler) AddImage(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	productID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in AddImageInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	img, err := h.svc.AddImage(c.UserContext(), id, productID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, img)
}

// DeleteImage removes a product image.
//
//	@Summary	Delete a product image
//	@Tags		products
//	@Security	BearerAuth
//	@Param		imageId	path	string	true	"Image id"
//	@Success	204
//	@Router		/products/images/{imageId} [delete]
func (h *Handler) DeleteImage(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	imageID, err := auth.ParseID(c, "imageId")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteImage(c.UserContext(), id, imageID); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ListCategories returns every category.
//
//	@Summary	List categories
//	@Tags		categories
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Category}
//	@Router		/categories [get]
func (h *Handler) ListCategories(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	cats, err := h.svc.ListCategories(c.UserContext(), id)
	if err != nil {
		return err
	}
	return shared.OK(c, cats)
}

// CreateCategory adds a category.
//
//	@Summary	Create a category
//	@Tags		categories
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		CategoryInput	true	"Category"
//	@Success	201		{object}	shared.Envelope{data=Category}
//	@Router		/categories [post]
func (h *Handler) CreateCategory(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in CategoryInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cat, err := h.svc.CreateCategory(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, cat)
}

// UpdateCategory edits a category.
//
//	@Summary	Update a category
//	@Tags		categories
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Category id"
//	@Param		payload	body		CategoryInput	true	"Category"
//	@Success	200		{object}	shared.Envelope{data=Category}
//	@Router		/categories/{id} [put]
func (h *Handler) UpdateCategory(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	catID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in CategoryInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cat, err := h.svc.UpdateCategory(c.UserContext(), id, catID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, cat)
}

// DeleteCategory soft-deletes a category.
//
//	@Summary	Delete a category
//	@Tags		categories
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Category id"
//	@Success	204
//	@Router		/categories/{id} [delete]
func (h *Handler) DeleteCategory(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	catID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteCategory(c.UserContext(), id, catID, h.actor(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ListUnits returns every unit of measure.
//
//	@Summary	List units of measure
//	@Tags		units
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Unit}
//	@Router		/units [get]
func (h *Handler) ListUnits(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	units, err := h.svc.ListUnits(c.UserContext(), id)
	if err != nil {
		return err
	}
	return shared.OK(c, units)
}

// CreateUnit adds a unit of measure.
//
//	@Summary	Create a unit of measure
//	@Tags		units
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		UnitInput	true	"Unit"
//	@Success	201		{object}	shared.Envelope{data=Unit}
//	@Router		/units [post]
func (h *Handler) CreateUnit(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in UnitInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	u, err := h.svc.CreateUnit(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, u)
}
