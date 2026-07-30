package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/rs/zerolog"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/sabin-bhattarai/ims-backend/migrations"
)

// NewDatabase opens the Postgres pool and verifies connectivity.
func NewDatabase(ctx context.Context, cfg Config, lg zerolog.Logger) (*gorm.DB, error) {
	level := gormlogger.Warn
	if !cfg.IsProduction() {
		level = gormlogger.Info
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
		Logger:                 newGormLogger(lg, level),
		SkipDefaultTransaction: true,
		NowFunc:                func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	pool, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access sql pool: %w", err)
	}
	pool.SetMaxOpenConns(cfg.DBMaxOpenConns)
	pool.SetMaxIdleConns(cfg.DBMaxIdleConns)
	pool.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// RunMigrations applies all pending migrations from the embedded FS. It is
// safe to call concurrently: golang-migrate takes an advisory lock.
func RunMigrations(db *gorm.DB, lg zerolog.Logger) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}

	pool, err := db.DB()
	if err != nil {
		return err
	}
	driver, err := migratepgx.WithInstance(pool, &migratepgx.Config{})
	if err != nil {
		return fmt.Errorf("migration driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("migrate init: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return err
	}
	lg.Info().Uint("schema_version", version).Bool("dirty", dirty).Msg("migrations applied")
	return nil
}

// gormLogger adapts GORM's logger interface onto zerolog.
type gormLogger struct {
	lg    zerolog.Logger
	level gormlogger.LogLevel
}

func newGormLogger(lg zerolog.Logger, level gormlogger.LogLevel) gormlogger.Interface {
	return &gormLogger{lg: lg.With().Str("component", "gorm").Logger(), level: level}
}

func (g *gormLogger) LogMode(l gormlogger.LogLevel) gormlogger.Interface {
	return &gormLogger{lg: g.lg, level: l}
}

func (g *gormLogger) Info(_ context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Info {
		g.lg.Info().Msgf(msg, args...)
	}
}

func (g *gormLogger) Warn(_ context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Warn {
		g.lg.Warn().Msgf(msg, args...)
	}
}

func (g *gormLogger) Error(_ context.Context, msg string, args ...any) {
	if g.level >= gormlogger.Error {
		g.lg.Error().Msgf(msg, args...)
	}
}

func (g *gormLogger) Trace(_ context.Context, begin time.Time, fc func() (string, int64), err error) {
	if g.level <= gormlogger.Silent {
		return
	}
	sql, rows := fc()
	elapsed := time.Since(begin)

	switch {
	case err != nil && !errors.Is(err, gorm.ErrRecordNotFound):
		g.lg.Error().Err(err).Str("sql", sql).Dur("elapsed", elapsed).Msg("query failed")
	case elapsed > 200*time.Millisecond:
		g.lg.Warn().Str("sql", sql).Int64("rows", rows).Dur("elapsed", elapsed).Msg("slow query")
	case g.level >= gormlogger.Info:
		g.lg.Debug().Str("sql", sql).Int64("rows", rows).Dur("elapsed", elapsed).Msg("query")
	}
}
