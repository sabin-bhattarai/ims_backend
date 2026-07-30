package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Cache wraps Redis with degradation-tolerant semantics. The uptime target
// requires the API to keep serving when Redis is down, so every method here
// reports failures without propagating them as request errors: callers treat a
// cache error as a miss and a rate-limiter error as "allow".
type Cache struct {
	client  *redis.Client
	ns      string
	lg      zerolog.Logger
	healthy bool
}

// NewCache dials Redis. A failed dial is logged and degrades the cache to
// no-op rather than aborting startup.
func NewCache(ctx context.Context, cfg Config, lg zerolog.Logger) *Cache {
	lg = lg.With().Str("component", "redis").Logger()

	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		lg.Error().Err(err).Msg("invalid REDIS_URL; running without cache")
		return &Cache{ns: cfg.RedisNamespace, lg: lg}
	}

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		lg.Warn().Err(err).Msg("redis unreachable; running in degraded mode")
		return &Cache{client: client, ns: cfg.RedisNamespace, lg: lg}
	}

	lg.Info().Msg("redis connected")
	return &Cache{client: client, ns: cfg.RedisNamespace, lg: lg, healthy: true}
}

// Client exposes the underlying client for components that need it (rate
// limiter storage). It may be nil when Redis was never reachable.
func (c *Cache) Client() *redis.Client { return c.client }

// Key namespaces a cache key.
func (c *Cache) Key(parts ...string) string {
	key := c.ns
	for _, p := range parts {
		key += ":" + p
	}
	return key
}

// Get returns the raw cached bytes, or ok=false on a miss or any failure.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool) {
	if c.client == nil {
		return nil, false
	}
	val, err := c.client.Get(ctx, key).Bytes()
	if err != nil {
		if err != redis.Nil {
			c.lg.Debug().Err(err).Str("key", key).Msg("cache get failed")
		}
		return nil, false
	}
	return val, true
}

// Set stores a value, swallowing failures.
func (c *Cache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if c.client == nil {
		return
	}
	if err := c.client.Set(ctx, key, val, ttl).Err(); err != nil {
		c.lg.Debug().Err(err).Str("key", key).Msg("cache set failed")
	}
}

// Delete removes keys, swallowing failures.
func (c *Cache) Delete(ctx context.Context, keys ...string) {
	if c.client == nil || len(keys) == 0 {
		return
	}
	if err := c.client.Del(ctx, keys...).Err(); err != nil {
		c.lg.Debug().Err(err).Msg("cache delete failed")
	}
}

// InvalidatePrefix drops every key under a prefix using SCAN, so a large
// keyspace is never blocked by KEYS.
func (c *Cache) InvalidatePrefix(ctx context.Context, prefix string) {
	if c.client == nil {
		return
	}
	iter := c.client.Scan(ctx, 0, prefix+"*", 200).Iterator()
	batch := make([]string, 0, 200)
	for iter.Next(ctx) {
		batch = append(batch, iter.Val())
		if len(batch) == cap(batch) {
			c.Delete(ctx, batch...)
			batch = batch[:0]
		}
	}
	c.Delete(ctx, batch...)
}

// Ping reports current reachability, used by the readiness probe.
func (c *Cache) Ping(ctx context.Context) error {
	if c.client == nil {
		return fmt.Errorf("redis not configured")
	}
	return c.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (c *Cache) Close() error {
	if c.client == nil {
		return nil
	}
	return c.client.Close()
}
