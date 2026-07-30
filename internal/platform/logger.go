package platform

import (
	"os"
	"time"

	"github.com/rs/zerolog"
)

// NewLogger builds the process-wide structured logger. Development gets human
// readable console output; production emits JSON for log shipping.
func NewLogger(cfg Config) zerolog.Logger {
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.TimeFieldFormat = time.RFC3339Nano

	var lg zerolog.Logger
	if cfg.IsProduction() {
		lg = zerolog.New(os.Stdout)
	} else {
		lg = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	}
	return lg.Level(level).With().
		Timestamp().
		Str("service", "ims-backend").
		Str("env", cfg.Env).
		Logger()
}
