package stock

import (
	"github.com/gofiber/fiber/v2"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// Handler exposes the stock HTTP endpoints.
type Handler struct {
	svc   *Service
	actor func(*fiber.Ctx) shared.Actor
}

// NewHandler builds the stock handler.
func NewHandler(svc *Service, actor func(*fiber.Ctx) shared.Actor) *Handler {
	return &Handler{svc: svc, actor: actor}
}

// ListItems returns on-hand stock rows.
//
//	@Summary	List stock levels
//	@Tags		stock
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[warehouse_id]	query		string	false	"Filter by warehouse"
//	@Param		filter[product_id]		query		string	false	"Filter by product"
//	@Param		filter[below_min]		query		bool	false	"Only items at or below their minimum"
//	@Param		filter[nonzero]			query		bool	false	"Exclude zero-quantity rows"
//	@Success	200						{object}	shared.Envelope{data=[]ItemView,meta=shared.PageMeta}
//	@Router		/stock/items [get]
func (h *Handler) ListItems(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListItems(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// Adjust applies a manual stock movement.
//
//	@Summary		Adjust stock
//	@Description	Signed quantity: positive receives, negative issues. Supply client_request_id to make the call idempotent.
//	@Tags			stock
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payload	body		AdjustInput	true	"Adjustment"
//	@Success		200		{object}	shared.Envelope{data=MovementResult}
//	@Failure		409		{object}	shared.ErrorEnvelope	"Insufficient stock"
//	@Router			/stock/adjust [post]
func (h *Handler) Adjust(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in AdjustInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	result, err := h.svc.Adjust(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, result)
}

// Sync applies a batch of offline-queued movements.
//
//	@Summary		Sync offline stock movements
//	@Description	Applies each queued movement independently and reports a per-item outcome, so one rejected entry does not discard the batch.
//	@Tags			stock
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			payload	body		SyncRequest	true	"Queued movements"
//	@Success		200		{object}	shared.Envelope{data=SyncResponse}
//	@Router			/stock/sync [post]
func (h *Handler) Sync(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in SyncRequest
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	resp, err := h.svc.Sync(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, resp)
}

// ListMovements returns the stock ledger.
//
//	@Summary	List stock movements
//	@Tags		stock
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[product_id]	query		string	false	"Filter by product"
//	@Param		filter[type]		query		string	false	"Filter by movement type"
//	@Success	200					{object}	shared.Envelope{data=[]Movement,meta=shared.PageMeta}
//	@Router		/stock/movements [get]
func (h *Handler) ListMovements(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListMovements(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// CreateBatch registers a batch/lot.
//
//	@Summary	Create a batch
//	@Tags		stock
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		BatchInput	true	"Batch"
//	@Success	201		{object}	shared.Envelope{data=Batch}
//	@Router		/stock/batches [post]
func (h *Handler) CreateBatch(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in BatchInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	b, err := h.svc.CreateBatch(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, b)
}

// ListBatches returns batches.
//
//	@Summary	List batches
//	@Tags		stock
//	@Produce	json
//	@Security	BearerAuth
//	@Param		filter[expiring_within_days]	query		int	false	"Only batches expiring within N days"
//	@Success	200								{object}	shared.Envelope{data=[]Batch,meta=shared.PageMeta}
//	@Router		/stock/batches [get]
func (h *Handler) ListBatches(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListBatches(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// CreateTransfer drafts a warehouse-to-warehouse transfer.
//
//	@Summary	Create a stock transfer
//	@Tags		transfers
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		TransferInput	true	"Transfer"
//	@Success	201		{object}	shared.Envelope{data=Transfer}
//	@Router		/stock/transfers [post]
func (h *Handler) CreateTransfer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in TransferInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	t, err := h.svc.CreateTransfer(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, t)
}

// ListTransfers returns a page of transfers.
//
//	@Summary	List stock transfers
//	@Tags		transfers
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]Transfer,meta=shared.PageMeta}
//	@Router		/stock/transfers [get]
func (h *Handler) ListTransfers(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListTransfers(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetTransfer returns one transfer.
//
//	@Summary	Get a stock transfer
//	@Tags		transfers
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Transfer id"
//	@Success	200	{object}	shared.Envelope{data=Transfer}
//	@Router		/stock/transfers/{id} [get]
func (h *Handler) GetTransfer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	tid, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	t, err := h.svc.GetTransfer(c.UserContext(), id, tid)
	if err != nil {
		return err
	}
	return shared.OK(c, t)
}

// DispatchTransfer removes stock from the source warehouse.
//
//	@Summary	Dispatch a stock transfer
//	@Tags		transfers
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Transfer id"
//	@Success	200	{object}	shared.Envelope{data=Transfer}
//	@Failure	409	{object}	shared.ErrorEnvelope
//	@Router		/stock/transfers/{id}/dispatch [post]
func (h *Handler) DispatchTransfer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	tid, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	t, err := h.svc.DispatchTransfer(c.UserContext(), id, tid, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, t)
}

// ReceiveTransfer adds stock at the destination warehouse.
//
//	@Summary	Receive a stock transfer
//	@Tags		transfers
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Transfer id"
//	@Success	200	{object}	shared.Envelope{data=Transfer}
//	@Router		/stock/transfers/{id}/receive [post]
func (h *Handler) ReceiveTransfer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	tid, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	t, err := h.svc.ReceiveTransfer(c.UserContext(), id, tid, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, t)
}

// CancelTransfer voids a draft transfer.
//
//	@Summary	Cancel a stock transfer
//	@Tags		transfers
//	@Security	BearerAuth
//	@Param		id	path	string	true	"Transfer id"
//	@Success	204
//	@Router		/stock/transfers/{id}/cancel [post]
func (h *Handler) CancelTransfer(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	tid, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	if err := h.svc.CancelTransfer(c.UserContext(), id, tid, h.actor(c)); err != nil {
		return err
	}
	return shared.NoContent(c)
}

// CreateCycleCount opens a stock-take.
//
//	@Summary	Create a cycle count
//	@Tags		cycle-counts
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		payload	body		CycleCountInput	true	"Cycle count scope"
//	@Success	201		{object}	shared.Envelope{data=CycleCount}
//	@Router		/stock/cycle-counts [post]
func (h *Handler) CreateCycleCount(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	var in CycleCountInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cc, err := h.svc.CreateCycleCount(c.UserContext(), id, in, h.actor(c))
	if err != nil {
		return err
	}
	return shared.Created(c, cc)
}

// ListCycleCounts returns a page of cycle counts.
//
//	@Summary	List cycle counts
//	@Tags		cycle-counts
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]CycleCount,meta=shared.PageMeta}
//	@Router		/stock/cycle-counts [get]
func (h *Handler) ListCycleCounts(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, meta, err := h.svc.ListCycleCounts(c.UserContext(), id, shared.ParseQuery(c))
	if err != nil {
		return err
	}
	return shared.List(c, rows, meta)
}

// GetCycleCount returns a count with its lines.
//
//	@Summary	Get a cycle count
//	@Tags		cycle-counts
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Cycle count id"
//	@Success	200	{object}	shared.Envelope{data=CycleCount}
//	@Router		/stock/cycle-counts/{id} [get]
func (h *Handler) GetCycleCount(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	ccID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	cc, err := h.svc.GetCycleCount(c.UserContext(), id, ccID)
	if err != nil {
		return err
	}
	return shared.OK(c, cc)
}

// RecordCount submits counted quantities.
//
//	@Summary	Record counted quantities
//	@Tags		cycle-counts
//	@Accept		json
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id		path		string				true	"Cycle count id"
//	@Param		payload	body		RecordCountInput	true	"Counted lines"
//	@Success	200		{object}	shared.Envelope{data=CycleCount}
//	@Router		/stock/cycle-counts/{id}/record [post]
func (h *Handler) RecordCount(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	ccID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	var in RecordCountInput
	if err := shared.BindAndValidate(c, &in); err != nil {
		return err
	}
	cc, err := h.svc.RecordCount(c.UserContext(), id, ccID, in)
	if err != nil {
		return err
	}
	return shared.OK(c, cc)
}

// CompleteCycleCount posts variances to the ledger.
//
//	@Summary	Complete a cycle count
//	@Tags		cycle-counts
//	@Produce	json
//	@Security	BearerAuth
//	@Param		id	path		string	true	"Cycle count id"
//	@Success	200	{object}	shared.Envelope{data=CycleCount}
//	@Failure	409	{object}	shared.ErrorEnvelope
//	@Router		/stock/cycle-counts/{id}/complete [post]
func (h *Handler) CompleteCycleCount(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	ccID, err := auth.ParseID(c, "id")
	if err != nil {
		return err
	}
	cc, err := h.svc.CompleteCycleCount(c.UserContext(), id, ccID, h.actor(c))
	if err != nil {
		return err
	}
	return shared.OK(c, cc)
}
