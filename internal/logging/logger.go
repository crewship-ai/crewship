package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// New creates a structured logger with the given level ("debug", "info", "warn",
// "error") and format ("json" or "text"), writing to w (defaults to os.Stdout).
func New(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler)
}

// WithContext returns a new context that carries the given logger.
func WithContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext extracts the logger from ctx, or returns slog.Default() if none is set.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// parseLevel resolves the configured log level. It accepts case variants
// ("DEBUG", "Debug") and the common synonyms "warning" and "fatal" so a
// typo like CREWSHIP_LOG_LEVEL=warning doesn't silently fall through to
// info-and-quiet. Truly unknown strings still default to info but emit a
// stderr notice — the logger isn't constructed yet so we can't go via
// slog itself.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal":
		return slog.LevelError
	default:
		fmt.Fprintf(os.Stderr, "logging: unknown level %q, defaulting to info\n", s)
		return slog.LevelInfo
	}
}
