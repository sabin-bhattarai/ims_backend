package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rs/zerolog"
)

// Task type names. Handlers register against these in the notification module.
const (
	TaskSendEmail        = "email:send"
	TaskPushNotification = "push:send"
	TaskLowStockScan     = "stock:low_stock_scan"
	TaskExpiryScan       = "stock:expiry_scan"
	TaskGenerateReport   = "report:generate"
)

// Queue publishes background jobs. Like Cache it degrades rather than fails:
// if Redis is unavailable an enqueue is logged and dropped so that, say,
// receiving stock still succeeds when the mail queue is down.
type Queue struct {
	client *asynq.Client
	lg     zerolog.Logger
}

// NewQueue builds an Asynq client from the Redis URL.
func NewQueue(cfg Config, lg zerolog.Logger) (*Queue, error) {
	lg = lg.With().Str("component", "queue").Logger()
	opts, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		lg.Error().Err(err).Msg("invalid REDIS_URL; background jobs disabled")
		return &Queue{lg: lg}, nil
	}
	return &Queue{client: asynq.NewClient(opts), lg: lg}, nil
}

// Enqueue serialises a payload and submits it for asynchronous processing.
func (q *Queue) Enqueue(ctx context.Context, taskType string, payload any, opts ...asynq.Option) {
	if q.client == nil {
		q.lg.Warn().Str("task", taskType).Msg("queue unavailable; task dropped")
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		q.lg.Error().Err(err).Str("task", taskType).Msg("marshal task payload")
		return
	}
	defaults := []asynq.Option{asynq.MaxRetry(5), asynq.Timeout(2 * time.Minute)}
	info, err := q.client.EnqueueContext(ctx, asynq.NewTask(taskType, body), append(defaults, opts...)...)
	if err != nil {
		q.lg.Error().Err(err).Str("task", taskType).Msg("enqueue failed; task dropped")
		return
	}
	q.lg.Debug().Str("task", taskType).Str("id", info.ID).Msg("task enqueued")
}

// Close shuts the publisher down.
func (q *Queue) Close() error {
	if q.client == nil {
		return nil
	}
	return q.client.Close()
}

// NewWorker builds the Asynq worker server used by cmd/worker.
func NewWorker(cfg Config, lg zerolog.Logger) (*asynq.Server, error) {
	opts, err := asynq.ParseRedisURI(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis uri: %w", err)
	}
	return asynq.NewServer(opts, asynq.Config{
		Concurrency: cfg.QueueConcurrency,
		Queues:      map[string]int{"critical": 6, "default": 3, "low": 1},
		Logger:      &asynqLogger{lg: lg.With().Str("component", "asynq").Logger()},
	}), nil
}

type asynqLogger struct{ lg zerolog.Logger }

func (a *asynqLogger) Debug(args ...any) { a.lg.Debug().Msg(fmt.Sprint(args...)) }
func (a *asynqLogger) Info(args ...any)  { a.lg.Info().Msg(fmt.Sprint(args...)) }
func (a *asynqLogger) Warn(args ...any)  { a.lg.Warn().Msg(fmt.Sprint(args...)) }
func (a *asynqLogger) Error(args ...any) { a.lg.Error().Msg(fmt.Sprint(args...)) }
func (a *asynqLogger) Fatal(args ...any) { a.lg.Fatal().Msg(fmt.Sprint(args...)) }
