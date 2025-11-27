package cli

import (
	"log/slog"
	"os"
)

// NewLogger creates a new slog.Logger based on the debug flag and log format
// It supports "json" and "text" formats, defaulting to "json" if an invalid format is provided
// This is a convenience function to standardize logger creation across the application
func NewLogger(debug bool, logFormat string) *slog.Logger {
	var logger *slog.Logger
	handlerOpts := &slog.HandlerOptions{}
	if debug {
		handlerOpts.Level = slog.LevelDebug
	}
	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
	case "text":
		logger = slog.New(slog.NewTextHandler(os.Stdout, handlerOpts))
	default:
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
		logger.Info("incorrect configuration for logFormat, using json handler")
	}
	return logger
}

// CronSlogAdapter adapts a slog.Logger to the interface expected by robfig/cron/v3
type CronSlogAdapter struct {
	*slog.Logger
}

// Info wires slog.Logger Info method to cron Logger interface
func (a *CronSlogAdapter) Info(msg string, keysAndValues ...interface{}) {
	a.Logger.Info(msg, keysAndValues...)
}

// Error wires slog.Logger Error method to cron Logger interface
func (a *CronSlogAdapter) Error(err error, msg string, keysAndValues ...interface{}) {
	// Prepend a key/value pair for the error.
	// You can name the key whatever you want; "error" is conventional.
	a.Logger.Error(msg, append([]any{"error", err.Error()}, keysAndValues...)...)
}
