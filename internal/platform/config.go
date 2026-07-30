// Package platform wires the process to the outside world: configuration,
// logging, Postgres, Redis and the background job queue.
package platform

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the fully resolved application configuration. Everything is
// supplied by environment variables (12-factor); see .env.example.
type Config struct {
	Env             string        `mapstructure:"ENV"`
	Port            int           `mapstructure:"PORT"`
	LogLevel        string        `mapstructure:"LOG_LEVEL"`
	ShutdownTimeout time.Duration `mapstructure:"SHUTDOWN_TIMEOUT"`

	DatabaseURL      string `mapstructure:"DATABASE_URL"`
	DBMaxOpenConns   int    `mapstructure:"DB_MAX_OPEN_CONNS"`
	DBMaxIdleConns   int    `mapstructure:"DB_MAX_IDLE_CONNS"`
	AutoMigrate      bool   `mapstructure:"AUTO_MIGRATE"`
	RedisURL         string `mapstructure:"REDIS_URL"`
	RedisNamespace   string `mapstructure:"REDIS_NAMESPACE"`
	QueueConcurrency int    `mapstructure:"QUEUE_CONCURRENCY"`

	JWTSecret        string        `mapstructure:"JWT_SECRET"`
	JWTIssuer        string        `mapstructure:"JWT_ISSUER"`
	AccessTokenTTL   time.Duration `mapstructure:"ACCESS_TOKEN_TTL"`
	RefreshTokenTTL  time.Duration `mapstructure:"REFRESH_TOKEN_TTL"`
	PasswordResetTTL time.Duration `mapstructure:"PASSWORD_RESET_TTL"`
	BcryptCost       int           `mapstructure:"BCRYPT_COST"`

	RateLimitRequests int           `mapstructure:"RATE_LIMIT_REQUESTS"`
	RateLimitWindow   time.Duration `mapstructure:"RATE_LIMIT_WINDOW"`
	CORSOrigins       string        `mapstructure:"CORS_ORIGINS"`

	SMTPHost      string `mapstructure:"SMTP_HOST"`
	SMTPPort      int    `mapstructure:"SMTP_PORT"`
	SMTPUsername  string `mapstructure:"SMTP_USERNAME"`
	SMTPPassword  string `mapstructure:"SMTP_PASSWORD"`
	SMTPFrom      string `mapstructure:"SMTP_FROM"`
	FCMServerKey  string `mapstructure:"FCM_SERVER_KEY"`
	WebAppBaseURL string `mapstructure:"WEB_APP_BASE_URL"`
}

// IsProduction reports whether the process is running with production
// guardrails (stricter config validation, no pretty logging).
func (c Config) IsProduction() bool { return strings.EqualFold(c.Env, "production") }

// AllowedOrigins splits the comma-separated CORS origin list.
func (c Config) AllowedOrigins() string { return c.CORSOrigins }

// LoadConfig reads configuration from the environment (and an optional .env
// file for local development), applies defaults, then validates.
func LoadConfig() (Config, error) {
	v := viper.New()
	v.AutomaticEnv()

	v.SetConfigFile(".env")
	v.SetConfigType("env")
	// A missing .env is expected everywhere except a developer laptop.
	_ = v.ReadInConfig()

	defaults := map[string]any{
		"ENV":                 "development",
		"PORT":                8080,
		"LOG_LEVEL":           "info",
		"SHUTDOWN_TIMEOUT":    "15s",
		"DB_MAX_OPEN_CONNS":   25,
		"DB_MAX_IDLE_CONNS":   5,
		"AUTO_MIGRATE":        true,
		"REDIS_NAMESPACE":     "ims",
		"QUEUE_CONCURRENCY":   10,
		"JWT_ISSUER":          "ims-backend",
		"ACCESS_TOKEN_TTL":    "15m",
		"REFRESH_TOKEN_TTL":   "720h",
		"PASSWORD_RESET_TTL":  "1h",
		"BCRYPT_COST":         12,
		"RATE_LIMIT_REQUESTS": 300,
		"RATE_LIMIT_WINDOW":   "1m",
		"CORS_ORIGINS":        "http://localhost:5173",
		"SMTP_PORT":           1025,
		"SMTP_FROM":           "no-reply@ims.local",
		"WEB_APP_BASE_URL":    "http://localhost:5173",
		"DATABASE_URL":        "postgres://ims:ims@localhost:5432/ims?sslmode=disable",
		"REDIS_URL":           "redis://localhost:6379/0",
	}
	for k, val := range defaults {
		v.SetDefault(k, val)
	}
	// viper only picks up env vars it knows about when a config file is absent.
	for k := range defaults {
		_ = v.BindEnv(k)
	}
	_ = v.BindEnv("JWT_SECRET")
	_ = v.BindEnv("SMTP_HOST")
	_ = v.BindEnv("SMTP_USERNAME")
	_ = v.BindEnv("SMTP_PASSWORD")
	_ = v.BindEnv("FCM_SERVER_KEY")

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return cfg, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		if c.IsProduction() {
			return fmt.Errorf("JWT_SECRET must be at least 32 characters in production")
		}
		if c.JWTSecret == "" {
			return fmt.Errorf("JWT_SECRET is required (generate with: openssl rand -hex 32)")
		}
	}
	if c.IsProduction() && c.AutoMigrate {
		// Migrations are applied by a deploy job, never by a booting replica:
		// N replicas racing to migrate is how you corrupt a schema.
		return fmt.Errorf("AUTO_MIGRATE must be false in production; run migrations as a deploy step")
	}
	return nil
}
