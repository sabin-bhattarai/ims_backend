package platform

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Health reports the state of each dependency.
type Health struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Checks  map[string]string `json:"checks"`
	Uptime  string            `json:"uptime"`
}

// HealthChecker answers the liveness and readiness probes.
type HealthChecker struct {
	db      *gorm.DB
	cache   *Cache
	version string
	started time.Time
	lg      zerolog.Logger
}

// NewHealthChecker builds a health checker.
func NewHealthChecker(db *gorm.DB, cache *Cache, version string, lg zerolog.Logger) *HealthChecker {
	return &HealthChecker{db: db, cache: cache, version: version, started: time.Now(), lg: lg}
}

// Live answers the liveness probe: is the process running at all.
//
//	@Summary	Liveness probe
//	@Tags		health
//	@Produce	json
//	@Success	200	{object}	Health
//	@Router		/health/live [get]
func (h *HealthChecker) Live(c *fiber.Ctx) error {
	return c.JSON(Health{
		Status: "ok", Version: h.version,
		Uptime: time.Since(h.started).Round(time.Second).String(),
		Checks: map[string]string{},
	})
}

// Ready answers the readiness probe.
//
// Postgres is required — without it the API cannot serve anything. Redis is not:
// the cache, rate limiter and queue all degrade gracefully, so a Redis outage
// reports "degraded" while the instance keeps taking traffic.
//
//	@Summary	Readiness probe
//	@Tags		health
//	@Produce	json
//	@Success	200	{object}	Health
//	@Failure	503	{object}	Health
//	@Router		/health/ready [get]
func (h *HealthChecker) Ready(c *fiber.Ctx) error {
	ctx, cancel := context.WithTimeout(c.UserContext(), 3*time.Second)
	defer cancel()

	report := Health{
		Status: "ok", Version: h.version,
		Uptime: time.Since(h.started).Round(time.Second).String(),
		Checks: map[string]string{},
	}
	status := fiber.StatusOK

	pool, err := h.db.DB()
	if err != nil || pool.PingContext(ctx) != nil {
		report.Checks["postgres"] = "unreachable"
		report.Status = "unavailable"
		status = fiber.StatusServiceUnavailable
	} else {
		report.Checks["postgres"] = "ok"
	}

	if err := h.cache.Ping(ctx); err != nil {
		report.Checks["redis"] = "unreachable"
		if report.Status == "ok" {
			report.Status = "degraded"
		}
	} else {
		report.Checks["redis"] = "ok"
	}

	return c.Status(status).JSON(report)
}
