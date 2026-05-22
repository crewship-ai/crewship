package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/lookout"
)

type ctxKey struct{}

// New creates a structured logger with the given level ("debug", "info", "warn",
// "error") and format ("json" or "text"), writing to w (defaults to os.Stdout).
//
// Every string-valued attribute (including the message-position values that
// land in groups) is piped through lookout.Redact -- the same secret
// detector the journal uses -- so a stray bearer token, OpenAI sk-... key,
// or `password=...` assignment in a log line is replaced by
// ***REDACTED:{kind}*** before it ever reaches stdout. The wiring is at
// the slog handler layer so it covers every caller in the binary without
// asking each call site to remember to redact.
func New(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{
		Level:       lvl,
		ReplaceAttr: redactAttr,
	}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	return slog.New(handler)
}

// redactAttr is slog's ReplaceAttr hook: invoked for every attribute on
// every log record. We scan only string-valued attrs (the other Kinds --
// Int64, Bool, Time, Duration -- can't carry a secret payload) and pass
// them through lookout.Redact. The findings slice is discarded here; the
// redacted string is what matters at the log layer.
//
// We deliberately leave the slog built-in time/level/source/msg keys
// alone: they have stable, non-sensitive shapes, and rewriting them
// would risk breaking downstream log parsers that assert on raw values.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	_ = groups
	switch a.Key {
	case slog.TimeKey, slog.LevelKey, slog.SourceKey, slog.MessageKey:
		return a
	}
	if a.Value.Kind() != slog.KindString {
		return a
	}
	s := a.Value.String()
	if s == "" {
		return a
	}
	redacted, _ := lookout.Redact(s)
	if redacted == s {
		return a
	}
	a.Value = slog.StringValue(redacted)
	return a
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
