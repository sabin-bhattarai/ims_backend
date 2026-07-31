package sales

import (
	"github.com/gofiber/fiber/v2"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Handler exposes the sales HTTP endpoints.
type Handler struct {
	svc   *Service
	actor func(*fiber.Ctx) shared.Actor
}

// NewHandler builds the sales handler.
func NewHandler(svc *Service, actor func(*fiber.Ctx) shared.Actor) *Handler {
	return &Handler{svc: svc, actor: actor}
}

// ListCustomers returns a page of customers.
//
//	@Summary	List customers
//	@Tags		customers
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Customer,meta=shared.PageMeta}
//	@Router		/customers [get]
func (h *Handler) ListCustomers(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListCustomers(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetCustomer returns one customer.
//
//	@Summary	Get a customer
//	@Tags		customers
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Customer id"
//	@Success	200	{object}	shared.Envelope{data=Customer}
//	@Router		/customers/{id} [get]
func (h *Handler) GetCustomer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	cID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	cust, err := h.svc.GetCustomer(c.UserContext(), id, cID)
	if err != nil {
		return err
	}
	return shared.OK(c, cust)
}

// CreateCustomer adds a customer.
//
//	@Summary	Create a customer
//	@Tags		customers
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		CustomerInput	true	"Customer"
//	@Success	201		{object}	shared.Envelope{data=Customer}
//	@Router		/customers [post]
func (h *Handler) CreateCustomer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in CustomerInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cust, err := h.svc.CreateCustomer(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, cust)
}

// UpdateCustomer edits a customer.
//
//	@Summary	Update a customer
//	@Tags		customers
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Customer id"
//	@Param		payload	body		CustomerInput	true	"Customer"
//	@Success	200		{object}	shared.Envelope{data=Customer}
//	@Router		/customers/{id} [put]
func (h *Handler) UpdateCustomer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	cID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in CustomerInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cust, err := h.svc.UpdateCustomer(c.UserContext(), id, cID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, cust)
}

// ListOrders returns a page of sales orders.
//
//	@Summary	List sales orders
//	@Tags		sales-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[status]	query		string	false	"Filter by status"
//	@Success	200				{object}	shared.Envelope{data=[]Order,meta=shared.PageMeta}
//	@Router		/sales-orders [get]
func (h *Handler) ListOrders(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListOrders(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetOrder returns one sales order.
//
//	@Summary	Get a sales order
//	@Tags		sales-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Sales order id"
//	@Success	200	{object}	shared.Envelope{data=Order}
//	@Router		/sales-orders/{id} [get]
func (h *Handler) GetOrder(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	order, err := h.svc.GetOrder(c.UserContext(), id, oID)
	if err != nil {
		return err
	}
	return shared.OK(c, order)
}

// CreateOrder drafts a sales order.
//
//	@Summary	Create a sales order
//	@Tags		sales-orders
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		OrderInput	true	"Sales order"
//	@Success	201		{object}	shared.Envelope{data=Order}
//	@Router		/sales-orders [post]
func (h *Handler) CreateOrder(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in OrderInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	order, err := h.svc.CreateOrder(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, order)
}

// Confirm reserves stock for the order.
//
//	@Summary	Confirm a sales order
//	@Tags		sales-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Sales order id"
//	@Success	200	{object}	shared.Envelope{data=Order}
//	@Failure	409	{object}	shared.ErrorEnvelope	"Insufficient stock"
//	@Router		/sales-orders/{id}/confirm [post]
func (h *Handler) Confirm(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	order, err := h.svc.Confirm(c.UserContext(), id, oID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, order)
}

// advanceRequest names the next fulfilment status.
type advanceRequest struct {
	Status OrderStatus `json:"status" validate:"required,oneof=picking packed delivered"`
}

// Advance moves the order through picking, packed or delivered.
//
//	@Summary	Advance a sales order's fulfilment status
//	@Tags		sales-orders
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Sales order id"
//	@Param		payload	body		advanceRequest	true	"Next status"
//	@Success	200		{object}	shared.Envelope{data=Order}
//	@Failure	409		{object}	shared.ErrorEnvelope
//	@Router		/sales-orders/{id}/advance [post]
func (h *Handler) Advance(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in advanceRequest
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	order, err := h.svc.Advance(c.UserContext(), id, oID, in.Status, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, order)
}

// Ship issues the stock for an order.
//
//	@Summary		Ship a sales order
//	@Description	Issues stock via FIFO, records COGS per line and releases the reservation.
//	@Tags			sales-orders
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string		true	"Sales order id"
//	@Param			payload	body		ShipInput	false	"Tracking number"
//	@Success		200		{object}	shared.Envelope{data=Order}
//	@Router			/sales-orders/{id}/ship [post]
func (h *Handler) Ship(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in ShipInput
	_ = c.BodyParser(&in) // the body is optional
	order, err := h.svc.Ship(c.UserContext(), id, oID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, order)
}

// Cancel voids an order.
//
//	@Summary	Cancel a sales order
//	@Tags		sales-orders
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Sales order id"
//	@Success	200	{object}	shared.Envelope{data=Order}
//	@Router		/sales-orders/{id}/cancel [post]
func (h *Handler) Cancel(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	order, err := h.svc.Cancel(c.UserContext(), id, oID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, order)
}

// IssueInvoice bills a shipped order.
//
//	@Summary	Issue an invoice for a sales order
//	@Tags		invoices
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string				true	"Sales order id"
//	@Param		payload	body		IssueInvoiceInput	true	"Invoice terms"
//	@Success	201		{object}	shared.Envelope{data=Invoice}
//	@Router		/sales-orders/{id}/invoice [post]
func (h *Handler) IssueInvoice(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	oID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in IssueInvoiceInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	inv, err := h.svc.IssueInvoice(c.UserContext(), id, oID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, inv)
}

// ListInvoices returns a page of invoices.
//
//	@Summary	List invoices
//	@Tags		invoices
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Invoice,meta=shared.PageMeta}
//	@Router		/invoices [get]
func (h *Handler) ListInvoices(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListInvoices(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// RecordPayment applies a payment to an invoice.
//
//	@Summary	Record a payment against an invoice
//	@Tags		invoices
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string				true	"Invoice id"
//	@Param		payload	body		RecordPaymentInput	true	"Payment"
//	@Success	200		{object}	shared.Envelope{data=Invoice}
//	@Router		/invoices/{id}/payments [post]
func (h *Handler) RecordPayment(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	invID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in RecordPaymentInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	inv, err := h.svc.RecordPayment(c.UserContext(), id, invID, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, inv)
}

// RequestReturn opens an RMA.
//
//	@Summary	Request a return
//	@Tags		returns
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		ReturnInput	true	"Return"
//	@Success	201		{object}	shared.Envelope{data=Return}
//	@Router		/returns [post]
func (h *Handler) RequestReturn(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in ReturnInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	ret, err := h.svc.RequestReturn(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, ret)
}

// ListReturns returns a page of RMAs.
//
//	@Summary	List returns
//	@Tags		returns
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Return,meta=shared.PageMeta}
//	@Router		/returns [get]
func (h *Handler) ListReturns(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListReturns(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// decideRequest carries an approval decision.
type decideRequest struct {
	Approve bool `json:"approve"`
}

// DecideReturn approves or rejects an RMA.
//
//	@Summary	Approve or reject a return
//	@Tags		returns
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string			true	"Return id"
//	@Param		payload	body		decideRequest	true	"Decision"
//	@Success	200		{object}	shared.Envelope{data=Return}
//	@Router		/returns/{id}/decide [post]
func (h *Handler) DecideReturn(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in decideRequest
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	ret, err := h.svc.DecideReturn(c.UserContext(), id, rID, in.Approve, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, ret)
}

// ReceiveReturn accepts returned goods, restocking when applicable.
//
//	@Summary	Receive returned goods
//	@Tags		returns
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Return id"
//	@Success	200	{object}	shared.Envelope{data=Return}
//	@Router		/returns/{id}/receive [post]
func (h *Handler) ReceiveReturn(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	ret, err := h.svc.ReceiveReturn(c.UserContext(), id, rID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, ret)
}
