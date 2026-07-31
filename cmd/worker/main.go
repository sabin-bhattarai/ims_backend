// Command worker runs the background job processor: emails, push
// notifications, and the scheduled low-stock and expiry scans.
//
// It is a separate binary from the API so the two scale independently — a
// report-generation backlog must not slow down barcode scans on the floor.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"

	"github.com/sabin-bhattarai/ims_backend/internal/notification"
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

	lg := platform.NewLogger(cfg).With().Str("process", "worker").Logger()
	lg.Info().Int("concurrency", cfg.QueueConcurrency).Msg("starting worker")

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

	queue, err := platform.NewQueue(cfg, lg)
	if err != nil {
		return err
	}
	defer func() { _ = queue.Close() }()

	notificationSvc := notification.NewService(db, queue, cfg, lg)
	worker := notification.NewWorker(db, notificationSvc, cfg, lg)

	server, err := platform.NewWorker(cfg, lg)
	if err != nil {
		return err
	}

	mux := asynq.NewServeMux()
	worker.Register(mux)

	// The periodic scans are driven by a scheduler so they run once per cluster
	// rather than once per replica.
	scheduler, err := newScheduler(cfg)
	if err != nil {
		return err
	}
	if err := scheduler.Start(); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer scheduler.Shutdown()

	errCh := make(chan error, 1)
	go func() {
		if err := server.Run(mux); err != nil {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("worker: %w", err)
	case <-ctx.Done():
		lg.Info().Msg("shutdown signal received")
	}

	server.Shutdown()
	lg.Info().Msg("worker stopped")
	return nil
}

// newScheduler registers the recurring stock scans.
func newScheduler(cfg platform.Config) (*asynq.Scheduler, error) {
	opts, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis uri: %w", err)
	}

	s := asynq.NewScheduler(opts, &asynq.SchedulerOpts{Location: time.UTC})

	// Hourly low-stock sweep and a daily expiry sweep at 07:00 UTC, before the
	// morning shift starts.
	if _, err := s.Register("@every 1h", asynq.NewTask(platform.TaskLowStockScan, nil)); err != nil {
		return nil, err
	}
	if _, err := s.Register("0 7 * * *", asynq.NewTask(platform.TaskExpiryScan, nil)); err != nil {
		return nil, err
	}
	return s, nil
}
