package cli

import (
	"log/slog"
	"os"
)

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

type CronSlogAdapter struct {
	*slog.Logger
}

func (a *CronSlogAdapter) Info(msg string, keysAndValues ...interface{}) {
	a.Logger.Info(msg, keysAndValues...)
}

func (a *CronSlogAdapter) Error(err error, msg string, keysAndValues ...interface{}) {
	// Prepend a key/value pair for the error.
	// You can name the key whatever you want; "error" is conventional.
	a.Logger.Error(msg, append([]any{"error", err.Error()}, keysAndValues...)...)
}
