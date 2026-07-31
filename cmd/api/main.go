// Command api runs the IMS HTTP server.
//
//	@title			IMS API
//	@version		1.0.0
//	@description	Inventory Management System REST API. Every response is wrapped in a `data` envelope; errors use an `error` envelope with a stable machine-readable code.
//	@BasePath		/api/v1
//
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Access token as "Bearer <token>". Obtain one from /auth/login and refresh it at /auth/refresh.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	// Imported for its side effect: registering the generated OpenAPI spec that
	// /docs serves. Regenerate with `make docs`.
	_ "github.com/sabin-bhattarai/ims_backend/docs"
	"github.com/sabin-bhattarai/ims_backend/internal/api"
	"github.com/sabin-bhattarai/ims_backend/internal/platform"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	lg := platform.NewLogger(cfg)
	lg.Info().Str("version", api.Version).Int("port", cfg.Port).Msg("starting ims-backend")

	// Signals cancel this context, which unwinds startup and shutdown alike.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := platform.NewDatabase(ctx, cfg, lg)
	if err != nil {
		return err
	}
	defer func() {
		if pool, err := db.DB(); err == nil {
			_ = pool.Close()
		}
	}()

	if cfg.AutoMigrate {
		// Development convenience only; LoadConfig refuses this in production,
		// where migrations run as a separate deploy step.
		if err := platform.RunMigrations(db, lg); err != nil {
			return err
		}
	}

	cache := platform.NewCache(ctx, cfg, lg)
	defer func() { _ = cache.Close() }()

	queue, err := platform.NewQueue(cfg, lg)
	if err != nil {
		return err
	}
	defer func() { _ = queue.Close() }()

	app := api.New(api.Deps{DB: db, Cache: cache, Queue: queue, Config: cfg, Logger: lg})

	serverErr := make(chan error, 1)
	go func() {
		addr := fmt.Sprintf(":%d", cfg.Port)
		lg.Info().Str("addr", addr).Msg("http server listening")
		if err := app.Fiber.Listen(addr); err != nil {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		lg.Info().Msg("shutdown signal received")
	}

	// Drain in-flight requests before exiting, so a rolling deploy does not
	// abort a stock adjustment mid-transaction.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := app.Fiber.ShutdownWithContext(shutdownCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			lg.Warn().Dur("timeout", cfg.ShutdownTimeout).Msg("forced shutdown; some requests were cut off")
		} else {
			return fmt.Errorf("shutdown: %w", err)
		}
	}

	lg.Info().Msg("shutdown complete")
	return nil
}
