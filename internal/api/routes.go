// Package api assembles the dependency graph and mounts every route.
package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/etag"
	"github.com/gofiber/swagger"
	"github.com/rs/zerolog"
	"gorm.io/gorm"

	"github.com/sabin-bhattarai/ims-backend/internal/auth"
	"github.com/sabin-bhattarai/ims-backend/internal/middleware"
	"github.com/sabin-bhattarai/ims-backend/internal/notification"
	"github.com/sabin-bhattarai/ims-backend/internal/platform"
	"github.com/sabin-bhattarai/ims-backend/internal/product"
	"github.com/sabin-bhattarai/ims-backend/internal/purchasing"
	"github.com/sabin-bhattarai/ims-backend/internal/reporting"
	"github.com/sabin-bhattarai/ims-backend/internal/sales"
	"github.com/sabin-bhattarai/ims-backend/internal/shared"
	"github.com/sabin-bhattarai/ims-backend/internal/stock"
	"github.com/sabin-bhattarai/ims-backend/internal/warehouse"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

// Application holds every constructed component, so main and the tests build
// the graph exactly the same way.
type Application struct {
	Fiber        *fiber.App
	Auth         *auth.Service
	Product      *product.Service
	Warehouse    *warehouse.Service
	Stock        *stock.Service
	Purchasing   *purchasing.Service
	Sales        *sales.Service
	Reporting    *reporting.Service
	Notification *notification.Service
	Tokens       *auth.TokenIssuer
}

// Deps are the external resources the application needs.
type Deps struct {
	DB     *gorm.DB
	Cache  *platform.Cache
	Queue  *platform.Queue
	Config platform.Config
	Logger zerolog.Logger
}

// New builds the application: services, handlers, middleware and routes.
func New(deps Deps) *Application {
	cfg, lg := deps.Config, deps.Logger

	app := fiber.New(fiber.Config{
		AppName:               "ims-backend",
		ErrorHandler:          middleware.ErrorHandler(lg),
		DisableStartupMessage: true,
		// A stock sync batch is the largest legitimate body; anything much
		// bigger is a mistake or an attack.
		BodyLimit:    4 * 1024 * 1024,
		ReadTimeout:  cfg.ShutdownTimeout,
		WriteTimeout: cfg.ShutdownTimeout,
		// Fiber sits behind a load balancer in every deployed environment, so
		// the client IP must come from the forwarded header for rate limiting
		// and audit logs to mean anything.
		ProxyHeader: fiber.HeaderXForwardedFor,
	})

	// --- shared infrastructure ---------------------------------------------
	auditor := shared.NewAuditor(deps.DB, lg)
	sequencer := shared.NewSequencer()
	tokens := auth.NewTokenIssuer(cfg)

	// --- services -----------------------------------------------------------
	notificationSvc := notification.NewService(deps.DB, deps.Queue, cfg, lg)
	authSvc := auth.NewService(deps.DB, cfg, tokens, auditor, notificationSvc, lg)
	productSvc := product.NewService(deps.DB, deps.Cache, auditor, lg)
	warehouseSvc := warehouse.NewService(deps.DB, auditor, lg)
	stockSvc := stock.NewService(deps.DB, stock.NewLedger(), auditor, notificationSvc, sequencer, lg)
	purchasingSvc := purchasing.NewService(deps.DB, stockSvc, auditor, sequencer, notificationSvc, lg)
	salesSvc := sales.NewService(deps.DB, stockSvc, auditor, sequencer, notificationSvc, lg)
	reportingSvc := reporting.NewService(deps.DB, deps.Cache, lg)

	// --- handlers -----------------------------------------------------------
	authH := auth.NewHandler(authSvc, middleware.Meta)
	productH := product.NewHandler(productSvc, middleware.Actor)
	warehouseH := warehouse.NewHandler(warehouseSvc, middleware.Actor)
	stockH := stock.NewHandler(stockSvc, middleware.Actor)
	purchasingH := purchasing.NewHandler(purchasingSvc, middleware.Actor)
	salesH := sales.NewHandler(salesSvc, middleware.Actor)
	reportingH := reporting.NewHandler(reportingSvc)
	notificationH := notification.NewHandler(notificationSvc)
	health := platform.NewHealthChecker(deps.DB, deps.Cache, Version, lg)

	// --- global middleware --------------------------------------------------
	app.Use(middleware.RequestID())
	app.Use(middleware.Recover(lg))
	app.Use(middleware.AccessLog(lg))
	app.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.AllowedOrigins(),
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization," + middleware.RequestIDHeader,
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowCredentials: true, // the refresh token travels in an httpOnly cookie
		ExposeHeaders:    middleware.RequestIDHeader,
	}))
	app.Use(compress.New())

	// --- probes and docs ----------------------------------------------------
	app.Get("/health/live", health.Live)
	app.Get("/health/ready", health.Ready)
	app.Get("/docs/*", swagger.HandlerDefault)

	requireAuth := middleware.RequireAuth(tokens)
	globalLimit := middleware.RateLimit(deps.Cache, middleware.RateLimitConfig{
		Requests: cfg.RateLimitRequests, Window: cfg.RateLimitWindow, KeyPrefix: "global",
	}, lg)

	v1 := app.Group("/api/v1", globalLimit)

	// --- auth ---------------------------------------------------------------
	// Credential endpoints get their own tighter bucket: the global limit is
	// sized for normal API traffic and would happily allow a password spray.
	loginLimit := middleware.RateLimit(deps.Cache, middleware.RateLimitConfig{
		Requests: 10, Window: cfg.RateLimitWindow, KeyPrefix: "auth",
	}, lg)

	authGroup := v1.Group("/auth")
	authGroup.Post("/register", loginLimit, authH.Register)
	authGroup.Post("/login", loginLimit, authH.Login)
	authGroup.Post("/refresh", authH.Refresh)
	authGroup.Post("/logout", authH.Logout)
	authGroup.Post("/forgot-password", loginLimit, authH.ForgotPassword)
	authGroup.Post("/reset-password", loginLimit, authH.ResetPassword)
	authGroup.Get("/me", requireAuth, authH.Me)
	authGroup.Post("/change-password", requireAuth, authH.ChangePassword)
	authGroup.Post("/logout-all", requireAuth, authH.LogoutAll)

	// Everything past this point requires a valid access token.
	api := v1.Group("", requireAuth)

	// --- users --------------------------------------------------------------
	users := api.Group("/users", middleware.RequirePermission(auth.PermUserManage))
	users.Get("/", authH.ListUsers)
	users.Post("/", authH.CreateUser)
	users.Get("/login-audits", authH.ListLoginAudits)
	users.Patch("/:id", authH.UpdateUser)
	users.Delete("/:id", authH.DeleteUser)

	// --- catalog ------------------------------------------------------------
	read := middleware.RequirePermission(auth.PermProductRead)
	write := middleware.RequirePermission(auth.PermProductWrite)

	products := api.Group("/products")
	products.Get("/", read, productH.List)
	// Registered before /:id so "scan" is never parsed as an id.
	products.Get("/scan/:code", read, productH.Scan)
	products.Post("/", write, productH.Create)
	products.Delete("/images/:imageId", write, productH.DeleteImage)
	products.Get("/:id", read, productH.Get)
	products.Patch("/:id", write, productH.Update)
	products.Delete("/:id", write, productH.Delete)
	products.Post("/:id/variants", write, productH.CreateVariant)
	products.Post("/:id/images", write, productH.AddImage)

	categories := api.Group("/categories")
	categories.Get("/", read, productH.ListCategories)
	categories.Post("/", write, productH.CreateCategory)
	categories.Put("/:id", write, productH.UpdateCategory)
	categories.Delete("/:id", write, productH.DeleteCategory)

	units := api.Group("/units")
	units.Get("/", read, productH.ListUnits)
	units.Post("/", write, productH.CreateUnit)

	// --- warehouses ---------------------------------------------------------
	whRead := middleware.RequirePermission(auth.PermWarehouseRead)
	whWrite := middleware.RequirePermission(auth.PermWarehouseWrite)

	warehouses := api.Group("/warehouses")
	warehouses.Get("/", whRead, warehouseH.List)
	warehouses.Post("/", whWrite, warehouseH.Create)
	warehouses.Delete("/locations/:locationId", whWrite, warehouseH.DeleteLocation)
	warehouses.Get("/:id", whRead, warehouseH.Get)
	warehouses.Put("/:id", whWrite, warehouseH.Update)
	warehouses.Delete("/:id", whWrite, warehouseH.Delete)
	warehouses.Get("/:id/locations", whRead, warehouseH.ListLocations)
	warehouses.Post("/:id/locations", whWrite, warehouseH.CreateLocation)

	// --- stock --------------------------------------------------------------
	stockRead := middleware.RequirePermission(auth.PermStockRead)
	stockAdjust := middleware.RequirePermission(auth.PermStockAdjust)
	stockTransfer := middleware.RequirePermission(auth.PermStockTransfer)
	stockCount := middleware.RequirePermission(auth.PermStockCount)

	stockGroup := api.Group("/stock")
	stockGroup.Get("/items", stockRead, stockH.ListItems)
	stockGroup.Get("/movements", stockRead, stockH.ListMovements)
	stockGroup.Post("/adjust", stockAdjust, stockH.Adjust)
	stockGroup.Post("/sync", stockAdjust, stockH.Sync)
	stockGroup.Get("/batches", stockRead, stockH.ListBatches)
	stockGroup.Post("/batches", stockAdjust, stockH.CreateBatch)

	transfers := stockGroup.Group("/transfers")
	transfers.Get("/", stockRead, stockH.ListTransfers)
	transfers.Post("/", stockTransfer, stockH.CreateTransfer)
	transfers.Get("/:id", stockRead, stockH.GetTransfer)
	transfers.Post("/:id/dispatch", stockTransfer, stockH.DispatchTransfer)
	transfers.Post("/:id/receive", stockTransfer, stockH.ReceiveTransfer)
	transfers.Post("/:id/cancel", stockTransfer, stockH.CancelTransfer)

	counts := stockGroup.Group("/cycle-counts")
	counts.Get("/", stockRead, stockH.ListCycleCounts)
	counts.Post("/", stockCount, stockH.CreateCycleCount)
	counts.Get("/:id", stockRead, stockH.GetCycleCount)
	counts.Post("/:id/record", stockCount, stockH.RecordCount)
	counts.Post("/:id/complete", stockCount, stockH.CompleteCycleCount)

	// --- purchasing ---------------------------------------------------------
	poRead := middleware.RequirePermission(auth.PermPurchaseRead)
	poWrite := middleware.RequirePermission(auth.PermPurchaseWrite)
	poApprove := middleware.RequirePermission(auth.PermPurchaseApprove)

	suppliers := api.Group("/suppliers")
	suppliers.Get("/", poRead, purchasingH.ListSuppliers)
	suppliers.Post("/", poWrite, purchasingH.CreateSupplier)
	suppliers.Get("/:id", poRead, purchasingH.GetSupplier)
	suppliers.Put("/:id", poWrite, purchasingH.UpdateSupplier)
	suppliers.Delete("/:id", poWrite, purchasingH.DeleteSupplier)

	pos := api.Group("/purchase-orders")
	pos.Get("/", poRead, purchasingH.ListPOs)
	pos.Post("/", poWrite, purchasingH.CreatePO)
	pos.Get("/:id", poRead, purchasingH.GetPO)
	pos.Post("/:id/submit", poWrite, purchasingH.SubmitPO)
	pos.Post("/:id/approve", poApprove, purchasingH.ApprovePO)
	pos.Post("/:id/reject", poApprove, purchasingH.RejectPO)
	pos.Post("/:id/cancel", poWrite, purchasingH.CancelPO)
	// Receiving is warehouse work, so it is gated on stock:adjust rather than
	// purchase:write — floor staff receive deliveries but never raise orders.
	pos.Post("/:id/receive", stockAdjust, purchasingH.Receive)
	pos.Get("/:id/receipts", poRead, purchasingH.ListReceipts)

	// --- sales --------------------------------------------------------------
	soRead := middleware.RequirePermission(auth.PermSalesRead)
	soWrite := middleware.RequirePermission(auth.PermSalesWrite)
	soShip := middleware.RequirePermission(auth.PermSalesShip)
	returnApprove := middleware.RequirePermission(auth.PermReturnApprove)

	customers := api.Group("/customers")
	customers.Get("/", soRead, salesH.ListCustomers)
	customers.Post("/", soWrite, salesH.CreateCustomer)
	customers.Get("/:id", soRead, salesH.GetCustomer)
	customers.Put("/:id", soWrite, salesH.UpdateCustomer)

	sos := api.Group("/sales-orders")
	sos.Get("/", soRead, salesH.ListOrders)
	sos.Post("/", soWrite, salesH.CreateOrder)
	sos.Get("/:id", soRead, salesH.GetOrder)
	sos.Post("/:id/confirm", soWrite, salesH.Confirm)
	sos.Post("/:id/advance", soShip, salesH.Advance)
	sos.Post("/:id/ship", soShip, salesH.Ship)
	sos.Post("/:id/cancel", soWrite, salesH.Cancel)
	sos.Post("/:id/invoice", soWrite, salesH.IssueInvoice)

	invoices := api.Group("/invoices")
	invoices.Get("/", soRead, salesH.ListInvoices)
	invoices.Post("/:id/payments", soWrite, salesH.RecordPayment)

	returns := api.Group("/returns")
	returns.Get("/", soRead, salesH.ListReturns)
	returns.Post("/", soWrite, salesH.RequestReturn)
	returns.Post("/:id/decide", returnApprove, salesH.DecideReturn)
	returns.Post("/:id/receive", stockAdjust, salesH.ReceiveReturn)

	// --- reporting ----------------------------------------------------------
	// Reports are read-only aggregates; ETag lets the dashboard poll cheaply.
	reports := api.Group("/reports", middleware.RequirePermission(auth.PermReportRead), etag.New())
	reports.Get("/dashboard", reportingH.Dashboard)
	reports.Get("/valuation", reportingH.Valuation)
	reports.Get("/turnover", reportingH.Turnover)
	reports.Get("/reorder", reportingH.Reorder)

	// --- notifications ------------------------------------------------------
	notifications := api.Group("/notifications")
	notifications.Get("/", notificationH.List)
	notifications.Post("/read-all", notificationH.MarkAllRead)
	notifications.Post("/:id/read", notificationH.MarkRead)
	notifications.Post("/devices", notificationH.RegisterDevice)
	notifications.Delete("/devices/:token", notificationH.UnregisterDevice)

	return &Application{
		Fiber: app, Auth: authSvc, Product: productSvc, Warehouse: warehouseSvc,
		Stock: stockSvc, Purchasing: purchasingSvc, Sales: salesSvc,
		Reporting: reportingSvc, Notification: notificationSvc, Tokens: tokens,
	}
}
