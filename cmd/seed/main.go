// Command seed loads a realistic demo dataset: one organization, four users
// covering every role, two warehouses with bin locations, a supplier and
// customer book, a product catalog, opening stock and a worked purchase order.
//
// It is idempotent at the organization level: re-running it against a database
// that already has the demo org exits without touching anything, so it is safe
// to wire into `make dev`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/platform"
	"github.com/sabin-bhattarai/ims_backend/internal/product"
	"github.com/sabin-bhattarai/ims_backend/internal/purchasing"
	"github.com/sabin-bhattarai/ims_backend/internal/sales"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
	"github.com/sabin-bhattarai/ims_backend/internal/stock"
	"github.com/sabin-bhattarai/ims_backend/internal/warehouse"
	"github.com/sabin-bhattarai/ims_backend/pkg/money"
)

// demoPassword is shared by every seeded account. Development only — the
// seeder refuses to run against a production config.
const demoPassword = "Password123!"

const demoOrgSlug = "acme-distribution-demo"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.IsProduction() {
		return errors.New("refusing to seed a production database")
	}

	lg := platform.NewLogger(cfg).With().Str("process", "seed").Logger()
	ctx := context.Background()

	db, err := platform.NewDatabase(ctx, cfg, lg)
	if err != nil {
		return err
	}
	if err := platform.RunMigrations(db, lg); err != nil {
		return err
	}

	var existing int64
	if err := db.Table("organizations").Where("slug = ?", demoOrgSlug).Count(&existing).Error; err != nil {
		return err
	}
	if existing > 0 {
		lg.Info().Msg("demo organization already present; nothing to do")
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(demoPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	ledger := stock.NewLedger()

	err = db.Transaction(func(tx *gorm.DB) error {
		org := auth.Organization{Name: "Acme Distribution (Demo)", Slug: demoOrgSlug, Currency: "USD"}
		if err := tx.Create(&org).Error; err != nil {
			return fmt.Errorf("create org: %w", err)
		}

		// --- users ---------------------------------------------------------
		people := []struct {
			name  string
			email string
			role  auth.Role
		}{
			{"Ada Admin", "admin@acme.test", auth.RoleAdmin},
			{"Morgan Manager", "manager@acme.test", auth.RoleManager},
			{"Wes Warehouse", "warehouse@acme.test", auth.RoleWarehouseStaff},
			{"Vic Viewer", "viewer@acme.test", auth.RoleViewer},
		}
		users := make(map[auth.Role]auth.User, len(people))
		for _, p := range people {
			u := auth.User{
				Email: p.email, FullName: p.name, PasswordHash: string(hash),
				Role: p.role, Status: auth.StatusActive,
			}
			u.OrganizationID = org.ID
			if err := tx.Create(&u).Error; err != nil {
				return fmt.Errorf("create user %s: %w", p.email, err)
			}
			users[p.role] = u
		}
		admin := users[auth.RoleAdmin]

		// --- units and categories ------------------------------------------
		units := []product.Unit{
			{Code: "EA", Name: "Each", Precision: 0},
			{Code: "BOX", Name: "Box", Precision: 0},
			{Code: "KG", Name: "Kilogram", Precision: 3},
		}
		for i := range units {
			units[i].OrganizationID = org.ID
			if err := tx.Create(&units[i]).Error; err != nil {
				return err
			}
		}

		categories := []product.Category{
			{Name: "Electronics"}, {Name: "Consumables"}, {Name: "Safety Equipment"},
		}
		for i := range categories {
			categories[i].OrganizationID = org.ID
			if err := tx.Create(&categories[i]).Error; err != nil {
				return err
			}
		}

		// --- warehouses and locations --------------------------------------
		warehouses := []warehouse.Warehouse{
			{Code: "MAIN", Name: "Main Distribution Centre", City: strptr("Kathmandu"), IsDefault: true, IsActive: true},
			{Code: "WEST", Name: "West Regional Depot", City: strptr("Pokhara"), IsActive: true},
		}
		for i := range warehouses {
			warehouses[i].OrganizationID = org.ID
			warehouses[i].ManagerID = &admin.ID
			if err := tx.Create(&warehouses[i]).Error; err != nil {
				return err
			}
		}

		// A small but real bin hierarchy, so location pickers have something to
		// show and the mobile app can exercise breadcrumbs.
		var bins []warehouse.Location
		for _, wh := range warehouses {
			zone := warehouse.Location{
				WarehouseID: wh.ID, Code: "A", Name: strptr("Zone A"),
				Kind: warehouse.KindZone, Path: strptr("A"), IsActive: true,
			}
			zone.OrganizationID = org.ID
			if err := tx.Create(&zone).Error; err != nil {
				return err
			}
			for _, code := range []string{"A-01", "A-02", "A-03"} {
				bin := warehouse.Location{
					WarehouseID: wh.ID, ParentID: &zone.ID, Code: code,
					Kind: warehouse.KindBin, Path: strptr("A/" + code), IsActive: true,
				}
				bin.OrganizationID = org.ID
				if err := tx.Create(&bin).Error; err != nil {
					return err
				}
				bins = append(bins, bin)
			}
		}

		// --- suppliers and customers ---------------------------------------
		suppliers := []purchasing.Supplier{
			{Code: "SUP-NORTH", Name: "Northwind Components", Email: strptr("sales@northwind.test"), LeadTimeDays: 7, IsActive: true},
			{Code: "SUP-GLOBE", Name: "Globex Consumables", Email: strptr("orders@globex.test"), LeadTimeDays: 14, IsActive: true},
		}
		for i := range suppliers {
			suppliers[i].OrganizationID = org.ID
			if err := tx.Create(&suppliers[i]).Error; err != nil {
				return err
			}
		}

		customers := []sales.Customer{
			{Code: "CUS-RETAIL", Name: "Everest Retail Group", Email: strptr("buying@everest.test"), CreditLimit: money.New(50000), IsActive: true},
			{Code: "CUS-TRADE", Name: "Annapurna Trading", Email: strptr("ops@annapurna.test"), CreditLimit: money.New(25000), IsActive: true},
		}
		for i := range customers {
			customers[i].OrganizationID = org.ID
			if err := tx.Create(&customers[i]).Error; err != nil {
				return err
			}
		}

		// --- products -------------------------------------------------------
		type productSpec struct {
			sku, name, barcode string
			category           int
			unit               int
			supplier           int
			cost, sell         float64
			min, max           float64
			reorder            float64
			trackBatches       bool
			openingStock       float64
		}
		specs := []productSpec{
			{"ELEC-KBD-001", "Mechanical Keyboard (Blue Switch)", "5901234123457", 0, 0, 0, 42.50, 79.99, 20, 200, 60, false, 145},
			{"ELEC-MOU-002", "Wireless Optical Mouse", "5901234123464", 0, 0, 0, 11.75, 24.99, 40, 400, 120, false, 320},
			{"ELEC-HUB-003", "7-Port USB-C Hub", "5901234123471", 0, 0, 0, 28.00, 59.00, 15, 120, 40, false, 18},
			{"CONS-TON-101", "Laser Toner Cartridge (Black)", "4006381333931", 1, 0, 1, 55.00, 99.00, 10, 80, 30, true, 46},
			{"CONS-PPR-102", "A4 Copy Paper (500 sheets)", "4006381333948", 1, 1, 1, 4.20, 8.50, 100, 1000, 400, false, 860},
			{"CONS-CLN-103", "Surface Cleaning Solution 5L", "4006381333955", 1, 2, 1, 9.90, 18.75, 25, 150, 50, true, 12},
			{"SAFE-HLM-201", "Hard Hat (Class E)", "8712345678905", 2, 0, 0, 17.40, 34.99, 30, 250, 80, false, 205},
			{"SAFE-GLV-202", "Cut-Resistant Gloves (Pair)", "8712345678912", 2, 0, 0, 6.15, 13.50, 60, 600, 200, false, 44},
		}

		created := make([]product.Product, 0, len(specs))
		for _, sp := range specs {
			maxStock := money.New(sp.max)
			p := product.Product{
				SKU: sp.sku, Name: sp.name, Barcode: strptr(sp.barcode),
				CategoryID: &categories[sp.category].ID,
				UnitID:     &units[sp.unit].ID,
				SupplierID: &suppliers[sp.supplier].ID,
				CostPrice:  money.New(sp.cost), SellPrice: money.New(sp.sell),
				MinStock: money.New(sp.min), MaxStock: &maxStock,
				ReorderQuantity: money.New(sp.reorder),
				TrackBatches:    sp.trackBatches,
				IsActive:        true,
			}
			p.OrganizationID = org.ID
			if sp.trackBatches {
				p.ShelfLifeDays = intptr(540)
			}
			if err := tx.Create(&p).Error; err != nil {
				return fmt.Errorf("create product %s: %w", sp.sku, err)
			}
			created = append(created, p)

			// --- opening stock ---------------------------------------------
			// Routed through the ledger rather than inserted directly, so the
			// demo data has a real movement history and correct FIFO layers.
			var batchID *uuid.UUID
			if sp.trackBatches {
				expiry := time.Now().UTC().AddDate(0, 0, 45) // deliberately near, to trigger expiry alerts
				b := stock.Batch{
					ProductID: p.ID, LotNumber: "LOT-" + p.SKU + "-A",
					ExpiryDate: &expiry, SupplierID: &suppliers[sp.supplier].ID,
					ReceivedAt: time.Now().UTC().AddDate(0, 0, -30),
				}
				b.OrganizationID = org.ID
				if err := tx.Create(&b).Error; err != nil {
					return err
				}
				batchID = &b.ID
			}

			cost := money.New(sp.cost)
			bin := bins[len(created)%len(bins)]
			if _, err := ledger.Apply(ctx, tx, stock.MovementRequest{
				OrgID: org.ID, Type: stock.MovementIn,
				ProductID: p.ID, WarehouseID: warehouses[0].ID,
				LocationID: &bin.ID, BatchID: batchID,
				Delta: money.New(sp.openingStock), UnitCost: &cost,
				Reason: "opening balance", ReferenceType: "seed",
				ActorID:    admin.ID,
				OccurredAt: time.Now().UTC().AddDate(0, 0, -30),
			}); err != nil {
				return fmt.Errorf("opening stock for %s: %w", sp.sku, err)
			}
		}

		// A second warehouse holding a little stock, so transfer and
		// multi-warehouse reports are not empty.
		for _, p := range created[:3] {
			cost := p.CostPrice
			if _, err := ledger.Apply(ctx, tx, stock.MovementRequest{
				OrgID: org.ID, Type: stock.MovementIn,
				ProductID: p.ID, WarehouseID: warehouses[1].ID,
				Delta: money.New(25), UnitCost: &cost,
				Reason: "opening balance", ReferenceType: "seed",
				ActorID:    admin.ID,
				OccurredAt: time.Now().UTC().AddDate(0, 0, -28),
			}); err != nil {
				return err
			}
		}

		// --- a worked purchase order ---------------------------------------
		// Map values are not addressable, so copy the ids the documents below
		// need to reference.
		managerID := users[auth.RoleManager].ID
		staffID := users[auth.RoleWarehouseStaff].ID

		seq := shared.NewSequencer()
		poCode, err := seq.Next(ctx, tx, org.ID, "purchase_order", "PO")
		if err != nil {
			return err
		}
		expected := time.Now().UTC().AddDate(0, 0, 7)
		po := purchasing.PurchaseOrder{
			Code: poCode, SupplierID: suppliers[0].ID, WarehouseID: warehouses[0].ID,
			Status: purchasing.StatusPendingApproval, Currency: "USD",
			ExpectedDate: &expected, CreatedBy: &managerID,
			SubmittedAt: timeptr(time.Now().UTC().AddDate(0, 0, -1)),
			Subtotal:    money.Zero(), TaxTotal: money.Zero(), DiscountTotal: money.Zero(),
			ShippingTotal: money.Zero(), GrandTotal: money.Zero(),
		}
		po.OrganizationID = org.ID

		poLines := make([]purchasing.PurchaseOrderLine, 0, 2)
		subtotal, taxTotal := money.Zero(), money.Zero()
		for _, p := range created[2:4] { // the two products that are low on stock
			qty := money.New(50)
			net, tax, total := money.LineTotal(qty, p.CostPrice, money.Zero(), money.New(13))
			poLines = append(poLines, purchasing.PurchaseOrderLine{
				ID: uuid.New(), ProductID: p.ID,
				QuantityOrdered: qty, QuantityReceived: money.Zero(),
				UnitPrice: p.CostPrice, TaxRate: money.New(13), DiscountRate: money.Zero(),
				LineTotal: total,
			})
			subtotal, taxTotal = subtotal.Add(net), taxTotal.Add(tax)
		}
		po.Subtotal, po.TaxTotal = money.Round(subtotal), money.Round(taxTotal)
		po.GrandTotal = money.Round(subtotal.Add(taxTotal))

		if err := tx.Create(&po).Error; err != nil {
			return err
		}
		for i := range poLines {
			poLines[i].PurchaseOrderID = po.ID
		}
		if err := tx.Create(&poLines).Error; err != nil {
			return err
		}

		// --- a shipped sales order, for turnover and margin reports ---------
		soCode, err := seq.Next(ctx, tx, org.ID, "sales_order", "SO")
		if err != nil {
			return err
		}
		so := sales.Order{
			Code: soCode, CustomerID: &customers[0].ID, WarehouseID: warehouses[0].ID,
			Status: sales.OrderDraft, Currency: "USD",
			OrderDate: time.Now().UTC().AddDate(0, 0, -7),
			Subtotal:  money.Zero(), TaxTotal: money.Zero(), DiscountTotal: money.Zero(),
			ShippingTotal: money.Zero(), GrandTotal: money.Zero(),
			CreatedBy: &managerID,
		}
		so.OrganizationID = org.ID

		soLines := make([]sales.OrderLine, 0, 2)
		soSubtotal, soTax := money.Zero(), money.Zero()
		for _, p := range created[:2] {
			qty := money.New(12)
			net, tax, total := money.LineTotal(qty, p.SellPrice, money.Zero(), money.New(13))
			soLines = append(soLines, sales.OrderLine{
				ID: uuid.New(), ProductID: p.ID,
				Quantity: qty, QuantityShipped: money.Zero(),
				UnitPrice: p.SellPrice, TaxRate: money.New(13), DiscountRate: money.Zero(),
				LineTotal: total,
			})
			soSubtotal, soTax = soSubtotal.Add(net), soTax.Add(tax)
		}
		so.Subtotal, so.TaxTotal = money.Round(soSubtotal), money.Round(soTax)
		so.GrandTotal = money.Round(soSubtotal.Add(soTax))

		if err := tx.Create(&so).Error; err != nil {
			return err
		}
		for i := range soLines {
			soLines[i].SalesOrderID = so.ID
		}
		if err := tx.Create(&soLines).Error; err != nil {
			return err
		}

		// Issue the stock so there is outbound movement history to report on.
		for _, line := range soLines {
			result, err := ledger.Apply(ctx, tx, stock.MovementRequest{
				OrgID: org.ID, Type: stock.MovementOut,
				ProductID: line.ProductID, WarehouseID: warehouses[0].ID,
				Delta:         line.Quantity.Neg(),
				Reason:        "sales order " + so.Code,
				ReferenceType: "sales_order", ReferenceID: &so.ID,
				ActorID:    staffID,
				OccurredAt: time.Now().UTC().AddDate(0, 0, -5),
			})
			if err != nil {
				return err
			}
			if err := tx.Model(&sales.OrderLine{}).Where("id = ?", line.ID).Updates(map[string]any{
				"quantity_shipped": line.Quantity, "cogs_total": result.COGS,
			}).Error; err != nil {
				return err
			}
		}
		shippedAt := time.Now().UTC().AddDate(0, 0, -5)
		if err := tx.Model(&sales.Order{}).Where("id = ?", so.ID).Updates(map[string]any{
			"status": sales.OrderShipped, "confirmed_at": so.OrderDate,
			"shipped_at": shippedAt, "tracking_number": "DEMO-TRACK-0001",
		}).Error; err != nil {
			return err
		}

		lg.Info().
			Str("organization", org.Name).
			Int("users", len(people)).
			Int("products", len(created)).
			Int("warehouses", len(warehouses)).
			Str("purchase_order", po.Code).
			Str("sales_order", so.Code).
			Msg("seed data created")
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Demo data ready. Sign in at the web app with any of:")
	for _, email := range []string{"admin@acme.test", "manager@acme.test", "warehouse@acme.test", "viewer@acme.test"} {
		fmt.Printf("  %-26s %s\n", email, demoPassword)
	}
	fmt.Println()
	return nil
}

func strptr(s string) *string        { return &s }
func intptr(i int) *int              { return &i }
func timeptr(t time.Time) *time.Time { return &t }
