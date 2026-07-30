package purchasing

import (
	"github.com/gofiber/fiber/v2"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
)

// Handler exposes the purchasing HTTP endpoints.
type Handler struct {
	svc   *Service
	actor func(*fiber.Ctx) shared.Actor
}

// NewHandler builds the purchasing handler.
func NewHandler(svc *Service, actor func(*fiber.Ctx) shared.Actor) *Handler {
	return &Handler{svc: svc, actor: actor}
}

// ListSuppliers returns a page of suppliers.
//
//	@Summary	List suppliers
//	@Tags		suppliers
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Supplier,meta=shared.PageMeta}
//	@Router		/suppliers [get]
func (h *Handler) ListSuppliers(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListSuppliers(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetSupplier returns one supplier.
//
//	@Summary	Get a supplier
//	@Tags		suppliers
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Supplier id"
//	@Success	200	{object}	shared.Envelope{data=Supplier}
//	@Router		/suppliers/{id} [get]
func (h *Handler) GetSupplier(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	sID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	sup, err := h.svc.GetSupplier(c.UserContext(), id, sID)
	if err != nil {
		return err
	}
	return shared.OK(c, sup)
}

// CreateSupplier adds a supplier.
//
//	@Summary	Create a supplier
//	@Tags		suppliers
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		SupplierInput	true	"Supplier"
//	@Success	201		{object}	shared.Envelope{data=Supplier}
//	@Router		/suppliers [post]
func (h *Handler) CreateSupplier(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in SupplierInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	sup, err := h.svc.CreateSupplier(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, sup)
}

// UpdateSupplier edits a supplier.
//
//	@Summary	Update a supplier
//	@Tags		suppliers
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Supplier id"
//	@Param		payload	body		SupplierInput	true	"Supplier"
//	@Success	200		{object}	shared.Envelope{data=Supplier}
//	@Router		/suppliers/{id} [put]
func (h *Handler) UpdateSupplier(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	sID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in SupplierInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	sup, err := h.svc.UpdateSupplier(c.UserContext(), id, sID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, sup)
}

// DeleteSupplier soft-deletes a supplier.
//
//	@Summary	Delete a supplier
//	@Tags		suppliers
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Supplier id"
//	@Success	204
//	@Router		/suppliers/{id} [delete]
func (h *Handler) DeleteSupplier(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	sID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteSupplier(c.UserContext(), id, sID, h.actor(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// ListPOs returns a page of purchase orders.
//
//	@Summary	List purchase orders
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[status]	query		string	false	"Filter by status"
//	@Success	200				{object}	shared.Envelope{data=[]PurchaseOrder,meta=shared.PageMeta}
//	@Router		/purchase-orders [get]
func (h *Handler) ListPOs(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListPOs(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetPO returns one purchase order.
//
//	@Summary	Get a purchase order
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Purchase order id"
//	@Success	200	{object}	shared.Envelope{data=PurchaseOrder}
//	@Router		/purchase-orders/{id} [get]
func (h *Handler) GetPO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	po, err := h.svc.GetPO(c.UserContext(), id, poID)
	if err != nil {
		return err
	}
	return shared.OK(c, po)
}

// CreatePO drafts a purchase order.
//
//	@Summary	Create a purchase order
//	@Tags		purchase-orders
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		POInput	true	"Purchase order"
//	@Success	201		{object}	shared.Envelope{data=PurchaseOrder}
//	@Router		/purchase-orders [post]
func (h *Handler) CreatePO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in POInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	po, err := h.svc.CreatePO(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, po)
}

// SubmitPO sends a draft for approval.
//
//	@Summary	Submit a purchase order for approval
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Purchase order id"
//	@Success	200	{object}	shared.Envelope{data=PurchaseOrder}
//	@Failure	409	{object}	shared.ErrorEnvelope
//	@Router		/purchase-orders/{id}/submit [post]
func (h *Handler) SubmitPO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	po, err := h.svc.SubmitPO(c.UserContext(), id, poID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, po)
}

// ApprovePO authorises a submitted order.
//
//	@Summary	Approve a purchase order
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Purchase order id"
//	@Success	200	{object}	shared.Envelope{data=PurchaseOrder}
//	@Failure	403	{object}	shared.ErrorEnvelope
//	@Router		/purchase-orders/{id}/approve [post]
func (h *Handler) ApprovePO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	po, err := h.svc.ApprovePO(c.UserContext(), id, poID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, po)
}

// RejectPO declines a submitted order.
//
//	@Summary	Reject a purchase order
//	@Tags		purchase-orders
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string		true	"Purchase order id"
//	@Param		payload	body		RejectInput	true	"Rejection reason"
//	@Success	200		{object}	shared.Envelope{data=PurchaseOrder}
//	@Router		/purchase-orders/{id}/reject [post]
func (h *Handler) RejectPO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in RejectInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	po, err := h.svc.RejectPO(c.UserContext(), id, poID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, po)
}

// CancelPO voids an order.
//
//	@Summary	Cancel a purchase order
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Purchase order id"
//	@Success	200	{object}	shared.Envelope{data=PurchaseOrder}
//	@Router		/purchase-orders/{id}/cancel [post]
func (h *Handler) CancelPO(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	po, err := h.svc.CancelPO(c.UserContext(), id, poID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, po)
}

// Receive books a delivery against a purchase order.
//
//	@Summary		Receive goods against a purchase order
//	@Description	Supports partial receipt; the order becomes partially_received until every line is satisfied.
//	@Tags			purchase-orders
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string			true	"Purchase order id"
//	@Param			payload	body		ReceiveInput	true	"Received lines"
//	@Success		201		{object}	shared.Envelope{data=GoodsReceipt}
//	@Failure		409		{object}	shared.ErrorEnvelope
//	@Router			/purchase-orders/{id}/receive [post]
func (h *Handler) Receive(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in ReceiveInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	grn, err := h.svc.Receive(c.UserContext(), id, poID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, grn)
}

// ListReceipts returns the receipts recorded against a purchase order.
//
//	@Summary	List goods receipts for a purchase order
//	@Tags		purchase-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Purchase order id"
//	@Success	200	{object}	shared.Envelope{data=[]GoodsReceipt}
//	@Router		/purchase-orders/{id}/receipts [get]
func (h *Handler) ListReceipts(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	poID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	rows, err := h.svc.ListReceipts(c.UserContext(), id, poID)
	if err != nil {
		return err
	}
	return shared.OK(c, rows)
}
