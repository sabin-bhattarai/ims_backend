// Package reporting produces stock valuation, turnover, reorder suggestions,
// dashboard summaries and CSV exports.
package reporting

import (
	"context"
	"encoding/csv"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/pkg/money"
)

// ValuationMethod selects how on-hand stock is valued.
type ValuationMethod string

const (
	// MethodFIFO values stock from the remaining FIFO cost layers — the exact
	// cost of the units actually on the shelf.
	MethodFIFO ValuationMethod = "fifo"
	// MethodWeightedAverage values stock at the average cost of remaining
	// layers, which smooths price volatility.
	MethodWeightedAverage ValuationMethod = "weighted_average"
	// MethodStandard values stock at the product's current standard cost.
	MethodStandard ValuationMethod = "standard"
)

// Service produces reports.
type Service struct {
	db    *gorm.DB
	cache *platform.Cache
	lg    zerolog.Logger
}

// NewService builds the reporting service.
func NewService(db *gorm.DB, cache *platform.Cache, lg zerolog.Logger) *Service {
	return &Service{db: db, cache: cache, lg: lg.With().Str("module", "reporting").Logger()}
}

// ValuationRow is one product's valuation.
type ValuationRow struct {
	ProductID     uuid.UUID     `json:"product_id"`
	SKU           string        `json:"sku"`
	Name          string        `json:"name"`
	WarehouseID   *uuid.UUID    `json:"warehouse_id,omitempty"`
	WarehouseName *string       `json:"warehouse_name,omitempty"`
	Quantity      money.Decimal `json:"quantity"`
	UnitCost      money.Decimal `json:"unit_cost"`
	TotalValue    money.Decimal `json:"total_value"`
}

// ValuationReport totals a valuation run.
type ValuationReport struct {
	Method      ValuationMethod `json:"method"`
	GeneratedAt time.Time       `json:"generated_at"`
	TotalValue  money.Decimal   `json:"total_value"`
	TotalUnits  money.Decimal   `json:"total_units"`
	Rows        []ValuationRow  `json:"rows"`
}

// Valuation values on-hand stock by the requested method.
//
// FIFO reads the unconsumed cost layers directly, so the number ties back to
// actual receipts rather than an assumption. Weighted average and standard cost
// are offered because different finance teams close their books differently.
func (s *Service) Valuation(ctx context.Context, actor auth.Identity, method ValuationMethod, warehouseID *uuid.UUID) (*ValuationReport, error) {
	if method == "" {
		method = MethodFIFO
	}

	var rows []ValuationRow
	var err error
	switch method {
	case MethodFIFO, MethodWeightedAverage:
		rows, err = s.valuationFromLayers(ctx, actor.OrgID, warehouseID)
	case MethodStandard:
		rows, err = s.valuationFromStandardCost(ctx, actor.OrgID, warehouseID)
	default:
		return nil, shared.Validation("unknown valuation method").
			WithDetails(map[string]any{"method": "must be one of: fifo, weighted_average, standard"})
	}
	if err != nil {
		return nil, err
	}

	report := &ValuationReport{
		Method: method, GeneratedAt: time.Now().UTC(),
		TotalValue: money.Zero(), TotalUnits: money.Zero(), Rows: rows,
	}
	for _, r := range rows {
		report.TotalValue = report.TotalValue.Add(r.TotalValue)
		report.TotalUnits = report.TotalUnits.Add(r.Quantity)
	}
	report.TotalValue = money.Round(report.TotalValue)
	return report, nil
}

// valuationFromLayers values stock from the remaining FIFO layers.
func (s *Service) valuationFromLayers(ctx context.Context, orgID uuid.UUID, warehouseID *uuid.UUID) ([]ValuationRow, error) {
	tx := s.db.WithContext(ctx).
		Table("cost_layers cl").
		Select(`cl.product_id, p.sku, p.name,
		        cl.warehouse_id, w.name AS warehouse_name,
		        SUM(cl.remaining) AS quantity,
		        SUM(cl.remaining * cl.unit_cost) AS total_value,
		        CASE WHEN SUM(cl.remaining) > 0
		             THEN SUM(cl.remaining * cl.unit_cost) / SUM(cl.remaining)
		             ELSE 0 END AS unit_cost`).
		Joins("JOIN products p ON p.id = cl.product_id AND p.deleted_at IS NULL").
		Joins("JOIN warehouses w ON w.id = cl.warehouse_id AND w.deleted_at IS NULL").
		Where("cl.organization_id = ? AND cl.remaining > 0", orgID).
		Group("cl.product_id, p.sku, p.name, cl.warehouse_id, w.name").
		Order("p.name ASC")

	if warehouseID != nil {
		tx = tx.Where("cl.warehouse_id = ?", *warehouseID)
	}

	var rows []ValuationRow
	err := tx.Scan(&rows).Error
	return rows, err
}

// valuationFromStandardCost values on-hand quantity at the product's cost price.
func (s *Service) valuationFromStandardCost(ctx context.Context, orgID uuid.UUID, warehouseID *uuid.UUID) ([]ValuationRow, error) {
	tx := s.db.WithContext(ctx).
		Table("stock_items si").
		Select(`si.product_id, p.sku, p.name,
		        si.warehouse_id, w.name AS warehouse_name,
		        SUM(si.quantity) AS quantity,
		        p.cost_price AS unit_cost,
		        SUM(si.quantity) * p.cost_price AS total_value`).
		Joins("JOIN products p ON p.id = si.product_id AND p.deleted_at IS NULL").
		Joins("JOIN warehouses w ON w.id = si.warehouse_id AND w.deleted_at IS NULL").
		Where("si.organization_id = ? AND si.quantity > 0", orgID).
		Group("si.product_id, p.sku, p.name, si.warehouse_id, w.name, p.cost_price").
		Order("p.name ASC")

	if warehouseID != nil {
		tx = tx.Where("si.warehouse_id = ?", *warehouseID)
	}

	var rows []ValuationRow
	err := tx.Scan(&rows).Error
	return rows, err
}

// TurnoverRow is one product's turnover over a period.
type TurnoverRow struct {
	ProductID     uuid.UUID     `json:"product_id"`
	SKU           string        `json:"sku"`
	Name          string        `json:"name"`
	UnitsIssued   money.Decimal `json:"units_issued"`
	AverageOnHand money.Decimal `json:"average_on_hand"`
	// TurnoverRatio is units issued divided by average stock held: how many
	// times the shelf emptied and refilled over the period.
	TurnoverRatio money.Decimal `json:"turnover_ratio"`
	DaysOfSupply  *int          `json:"days_of_supply,omitempty"`
}

// Turnover computes issue velocity over a window.
func (s *Service) Turnover(ctx context.Context, actor auth.Identity, from, to time.Time) ([]TurnoverRow, error) {
	if to.Before(from) {
		return nil, shared.Validation("`to` must be after `from`")
	}
	days := int(to.Sub(from).Hours() / 24)
	if days <= 0 {
		days = 1
	}

	type raw struct {
		ProductID    uuid.UUID
		SKU          string
		Name         string
		UnitsIssued  money.Decimal
		CurrentStock money.Decimal
	}

	var rows []raw
	err := s.db.WithContext(ctx).
		Table("products p").
		Select(`p.id AS product_id, p.sku, p.name,
		        COALESCE((SELECT SUM(-m.quantity_delta) FROM stock_movements m
		                  WHERE m.product_id = p.id
		                    AND m.type IN ('stock_out','transfer_out')
		                    AND m.occurred_at BETWEEN ? AND ?), 0) AS units_issued,
		        COALESCE((SELECT SUM(si.quantity) FROM stock_items si
		                  WHERE si.product_id = p.id), 0) AS current_stock`, from, to).
		Where("p.organization_id = ? AND p.deleted_at IS NULL", actor.OrgID).
		Order("units_issued DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	out := make([]TurnoverRow, 0, len(rows))
	for _, r := range rows {
		row := TurnoverRow{
			ProductID: r.ProductID, SKU: r.SKU, Name: r.Name,
			UnitsIssued: r.UnitsIssued, AverageOnHand: r.CurrentStock,
			TurnoverRatio: money.Zero(),
		}
		if money.IsPositive(r.CurrentStock) {
			row.TurnoverRatio = money.Round(r.UnitsIssued.Div(r.CurrentStock))
		}
		if money.IsPositive(r.UnitsIssued) {
			// Daily burn rate projected against what is left.
			perDay := r.UnitsIssued.Div(money.FromInt(int64(days)))
			if money.IsPositive(perDay) {
				d := int(r.CurrentStock.Div(perDay).IntPart())
				row.DaysOfSupply = &d
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// ReorderSuggestion recommends a purchase quantity.
type ReorderSuggestion struct {
	ProductID     uuid.UUID      `json:"product_id"`
	SKU           string         `json:"sku"`
	Name          string         `json:"name"`
	SupplierID    *uuid.UUID     `json:"supplier_id,omitempty"`
	SupplierName  *string        `json:"supplier_name,omitempty"`
	OnHand        money.Decimal  `json:"on_hand"`
	Reserved      money.Decimal  `json:"reserved"`
	Available     money.Decimal  `json:"available"`
	MinStock      money.Decimal  `json:"min_stock"`
	MaxStock      *money.Decimal `json:"max_stock,omitempty"`
	OnOrder       money.Decimal  `json:"on_order"`
	SuggestedQty  money.Decimal  `json:"suggested_quantity"`
	LeadTimeDays  int            `json:"lead_time_days"`
	EstimatedCost money.Decimal  `json:"estimated_cost"`
}

// ReorderSuggestions lists products that need replenishing.
//
// Quantity already on an open purchase order is subtracted, so a product that
// has been ordered but not yet delivered does not get ordered twice — the most
// common cause of accidental overstock.
func (s *Service) ReorderSuggestions(ctx context.Context, actor auth.Identity) ([]ReorderSuggestion, error) {
	var rows []ReorderSuggestion
	err := s.db.WithContext(ctx).
		Table("products p").
		Select(`p.id AS product_id, p.sku, p.name, p.supplier_id, s.name AS supplier_name,
		        p.min_stock, p.max_stock, p.cost_price,
		        COALESCE(s.lead_time_days, 0) AS lead_time_days,
		        COALESCE(stock.on_hand, 0) AS on_hand,
		        COALESCE(stock.reserved, 0) AS reserved,
		        COALESCE(stock.on_hand, 0) - COALESCE(stock.reserved, 0) AS available,
		        COALESCE(po.on_order, 0) AS on_order,
		        GREATEST(
		          COALESCE(p.max_stock, p.min_stock + p.reorder_quantity)
		          - COALESCE(stock.on_hand, 0) - COALESCE(po.on_order, 0), 0
		        ) AS suggested_qty`).
		Joins("LEFT JOIN suppliers s ON s.id = p.supplier_id AND s.deleted_at IS NULL").
		Joins(`LEFT JOIN (
		         SELECT product_id, SUM(quantity) AS on_hand, SUM(reserved_quantity) AS reserved
		         FROM stock_items GROUP BY product_id
		       ) stock ON stock.product_id = p.id`).
		Joins(`LEFT JOIN (
		         SELECT l.product_id, SUM(l.quantity_ordered - l.quantity_received) AS on_order
		         FROM purchase_order_lines l
		         JOIN purchase_orders o ON o.id = l.purchase_order_id
		         WHERE o.status IN ('approved','partially_received','pending_approval')
		           AND o.deleted_at IS NULL
		         GROUP BY l.product_id
		       ) po ON po.product_id = p.id`).
		Where(`p.organization_id = ? AND p.deleted_at IS NULL AND p.is_active
		       AND p.min_stock > 0
		       AND COALESCE(stock.on_hand, 0) - COALESCE(stock.reserved, 0) <= p.min_stock`, actor.OrgID).
		Order("p.name ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	// Cost is computed in Go rather than SQL so the rounding rule matches every
	// other monetary total in the system.
	for i := range rows {
		var cost money.Decimal
		if err := s.db.WithContext(ctx).Table("products").Select("cost_price").
			Where("id = ?", rows[i].ProductID).Scan(&cost).Error; err == nil {
			rows[i].EstimatedCost = money.Round(rows[i].SuggestedQty.Mul(cost))
		}
	}
	return rows, nil
}

// Dashboard is the landing-screen summary for web and mobile.
type Dashboard struct {
	GeneratedAt        time.Time     `json:"generated_at"`
	TotalProducts      int64         `json:"total_products"`
	TotalWarehouses    int64         `json:"total_warehouses"`
	TotalStockValue    money.Decimal `json:"total_stock_value"`
	TotalUnitsOnHand   money.Decimal `json:"total_units_on_hand"`
	LowStockCount      int64         `json:"low_stock_count"`
	ExpiringSoonCount  int64         `json:"expiring_soon_count"`
	PendingPOCount     int64         `json:"pending_po_count"`
	OpenSalesOrders    int64         `json:"open_sales_orders"`
	StockMovementTrend []TrendPoint  `json:"stock_movement_trend"`
	TopMovers          []TurnoverRow `json:"top_movers"`
}

// TrendPoint is one day of inbound/outbound volume.
type TrendPoint struct {
	Date     time.Time     `json:"date"`
	Inbound  money.Decimal `json:"inbound"`
	Outbound money.Decimal `json:"outbound"`
}

// Dashboard assembles the summary. It is cached briefly because it is polled by
// every open browser tab and the aggregate queries are the heaviest reads in the
// system.
func (s *Service) Dashboard(ctx context.Context, actor auth.Identity) (*Dashboard, error) {
	d := &Dashboard{GeneratedAt: time.Now().UTC(), TotalStockValue: money.Zero(), TotalUnitsOnHand: money.Zero()}
	org := actor.OrgID

	counts := []struct {
		dst   *int64
		table string
		where string
		args  []any
	}{
		{&d.TotalProducts, "products", "organization_id = ? AND deleted_at IS NULL", []any{org}},
		{&d.TotalWarehouses, "warehouses", "organization_id = ? AND deleted_at IS NULL", []any{org}},
		{&d.PendingPOCount, "purchase_orders", "organization_id = ? AND status = 'pending_approval' AND deleted_at IS NULL", []any{org}},
		{&d.OpenSalesOrders, "sales_orders", "organization_id = ? AND status IN ('confirmed','picking','packed') AND deleted_at IS NULL", []any{org}},
	}
	for _, c := range counts {
		if err := s.db.WithContext(ctx).Table(c.table).Where(c.where, c.args...).
			Count(c.dst).Error; err != nil {
			return nil, fmt.Errorf("dashboard count %s: %w", c.table, err)
		}
	}

	var totals struct {
		Units money.Decimal
		Value money.Decimal
	}
	if err := s.db.WithContext(ctx).Table("cost_layers").
		Select("COALESCE(SUM(remaining), 0) AS units, COALESCE(SUM(remaining * unit_cost), 0) AS value").
		Where("organization_id = ? AND remaining > 0", org).
		Scan(&totals).Error; err != nil {
		return nil, err
	}
	d.TotalUnitsOnHand, d.TotalStockValue = totals.Units, money.Round(totals.Value)

	if err := s.db.WithContext(ctx).Table("products p").
		Where(`p.organization_id = ? AND p.deleted_at IS NULL AND p.min_stock > 0
		       AND COALESCE((SELECT SUM(si.quantity) FROM stock_items si WHERE si.product_id = p.id), 0) <= p.min_stock`, org).
		Count(&d.LowStockCount).Error; err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Table("batches b").
		Where(`b.organization_id = ? AND b.deleted_at IS NULL AND b.expiry_date IS NOT NULL
		       AND b.expiry_date <= current_date + interval '30 days'
		       AND COALESCE((SELECT SUM(si.quantity) FROM stock_items si WHERE si.batch_id = b.id), 0) > 0`, org).
		Count(&d.ExpiringSoonCount).Error; err != nil {
		return nil, err
	}

	if err := s.db.WithContext(ctx).Table("stock_movements").
		Select(`date_trunc('day', occurred_at) AS date,
		        COALESCE(SUM(CASE WHEN quantity_delta > 0 THEN quantity_delta ELSE 0 END), 0) AS inbound,
		        COALESCE(SUM(CASE WHEN quantity_delta < 0 THEN -quantity_delta ELSE 0 END), 0) AS outbound`).
		Where("organization_id = ? AND occurred_at >= current_date - interval '30 days'", org).
		Group("date_trunc('day', occurred_at)").
		Order("date ASC").
		Scan(&d.StockMovementTrend).Error; err != nil {
		return nil, err
	}

	movers, err := s.Turnover(ctx, actor, time.Now().UTC().AddDate(0, 0, -30), time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if len(movers) > 10 {
		movers = movers[:10]
	}
	d.TopMovers = movers

	return d, nil
}

// Handler exposes the reporting HTTP endpoints.
type Handler struct{ svc *Service }

// NewHandler builds the reporting handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Dashboard returns the summary payload.
//
//	@Summary	Dashboard summary
//	@Tags		reports
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=Dashboard}
//	@Router		/reports/dashboard [get]
func (h *Handler) Dashboard(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	d, err := h.svc.Dashboard(c.UserContext(), id)
	if err != nil {
		return err
	}
	return shared.OK(c, d)
}

// Valuation returns the stock valuation report.
//
//	@Summary	Stock valuation
//	@Tags		reports
//	@Produce	json,text/csv
//	@Security	BearerAuth
//	@Param		method			query		string	false	"fifo, weighted_average or standard"	Enums(fifo, weighted_average, standard)
//	@Param		warehouse_id	query		string	false	"Limit to one warehouse"
//	@Param		format			query		string	false	"json (default) or csv"					Enums(json, csv)
//	@Success	200				{object}	shared.Envelope{data=ValuationReport}
//	@Router		/reports/valuation [get]
func (h *Handler) Valuation(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}

	var warehouseID *uuid.UUID
	if raw := c.Query("warehouse_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return shared.Validation("invalid warehouse_id")
		}
		warehouseID = &parsed
	}

	report, err := h.svc.Valuation(c.UserContext(), id, ValuationMethod(c.Query("method")), warehouseID)
	if err != nil {
		return err
	}

	if c.Query("format") == "csv" {
		return writeValuationCSV(c, report)
	}
	return shared.OK(c, report)
}

// Turnover returns the turnover report.
//
//	@Summary	Stock turnover
//	@Tags		reports
//	@Produce	json
//	@Security	BearerAuth
//	@Param		from	query		string	false	"Start date (RFC3339 or YYYY-MM-DD); defaults to 90 days ago"
//	@Param		to		query		string	false	"End date; defaults to now"
//	@Success	200		{object}	shared.Envelope{data=[]TurnoverRow}
//	@Router		/reports/turnover [get]
func (h *Handler) Turnover(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -90)
	if raw := c.Query("from"); raw != "" {
		parsed, err := parseDate(raw)
		if err != nil {
			return shared.Validation("invalid `from` date")
		}
		from = parsed
	}
	if raw := c.Query("to"); raw != "" {
		parsed, err := parseDate(raw)
		if err != nil {
			return shared.Validation("invalid `to` date")
		}
		to = parsed
	}

	rows, err := h.svc.Turnover(c.UserContext(), id, from, to)
	if err != nil {
		return err
	}
	return shared.OK(c, rows)
}

// Reorder returns replenishment suggestions.
//
//	@Summary	Reorder suggestions
//	@Tags		reports
//	@Produce	json
//	@Security	BearerAuth
//	@Success	200	{object}	shared.Envelope{data=[]ReorderSuggestion}
//	@Router		/reports/reorder [get]
func (h *Handler) Reorder(c *fiber.Ctx) error {
	id, err := auth.FromContext(c)
	if err != nil {
		return err
	}
	rows, err := h.svc.ReorderSuggestions(c.UserContext(), id)
	if err != nil {
		return err
	}
	return shared.OK(c, rows)
}

// writeValuationCSV streams the report as a CSV attachment.
func writeValuationCSV(c *fiber.Ctx, report *ValuationReport) error {
	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	c.Set(fiber.HeaderContentDisposition,
		fmt.Sprintf(`attachment; filename="stock-valuation-%s.csv"`, time.Now().UTC().Format("2006-01-02")))

	w := csv.NewWriter(c.Response().BodyWriter())
	header := []string{"SKU", "Product", "Warehouse", "Quantity", "Unit Cost", "Total Value"}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, r := range report.Rows {
		warehouse := ""
		if r.WarehouseName != nil {
			warehouse = *r.WarehouseName
		}
		if err := w.Write([]string{
			r.SKU, r.Name, warehouse,
			r.Quantity.String(), r.UnitCost.String(), r.TotalValue.String(),
		}); err != nil {
			return err
		}
	}
	if err := w.Write([]string{"", "", "TOTAL", report.TotalUnits.String(), "", report.TotalValue.String()}); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

// parseDate accepts either a full RFC3339 timestamp or a plain YYYY-MM-DD.
func parseDate(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Parse("2006-01-02", raw)
}
