package middleware

import (
	"context"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/sabin-bhattarai/ims_backend/internal/auth"
	"github.com/sabin-bhattarai/ims_backend/internal/platform"
	"github.com/sabin-bhattarai/ims_backend/internal/shared"
)

// RateLimitConfig configures a limiter instance.
type RateLimitConfig struct {
	// Requests permitted per Window.
	Requests int
	Window   time.Duration
	// KeyPrefix separates independent buckets, e.g. "global" vs "login".
	KeyPrefix string
}

// RateLimit applies a fixed-window limit backed by Redis, so the limit holds
// across every API replica rather than per-process.
//
// If Redis is unavailable the request is allowed through: the uptime target
// makes "fail open" the right trade-off for a limiter, since failing closed
// would turn a cache outage into a total outage.
func RateLimit(cache *platform.Cache, cfg RateLimitConfig, lg zerolog.Logger) fiber.Handler {
	lg = lg.With().Str("component", "ratelimit").Logger()
	if cfg.Requests <= 0 {
		cfg.Requests = 300
	}
	if cfg.Window <= 0 {
		cfg.Window = time.Minute
	}

	return func(c *fiber.Ctx) error {
		client := cache.Client()
		if client == nil {
			return c.Next()
		}

		key := cache.Key("ratelimit", cfg.KeyPrefix, rateLimitSubject(c))
		ctx, cancel := context.WithTimeout(c.UserContext(), 100*time.Millisecond)
		defer cancel()

		count, err := client.Incr(ctx, key).Result()
		if err != nil {
			lg.Debug().Err(err).Msg("rate limit check failed; allowing request")
			return c.Next()
		}
		if count == 1 {
			// First hit in this window: start the expiry clock.
			if err := client.Expire(ctx, key, cfg.Window).Err(); err != nil {
				lg.Debug().Err(err).Msg("failed to set rate limit window")
			}
		}

		remaining := max(cfg.Requests-int(count), 0)
		c.Set("X-RateLimit-Limit", strconv.Itoa(cfg.Requests))
		c.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if int(count) > cfg.Requests {
			ttl, _ := client.TTL(ctx, key).Result()
			if ttl <= 0 {
				ttl = cfg.Window
			}
			c.Set("Retry-After", strconv.Itoa(int(ttl.Seconds())))
			return shared.
				RateLimited("too many requests").
				WithDetails(map[string]any{"retry_after_seconds": int(ttl.Seconds())})
		}
		return c.Next()
	}
}

// rateLimitSubject buckets authenticated callers by user id and anonymous ones
// by IP, so one noisy tenant cannot exhaust another tenant's allowance.
func rateLimitSubject(c *fiber.Ctx) string {
	if id := auth.MustIdentity(c); id.UserID != uuid.Nil {
		return "user:" + id.UserID.String()
	}
	return "ip:" + c.IP()
}
