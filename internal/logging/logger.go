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

	// Bind the handler to the process-wide runtime level (a slog.Leveler)
	// rather than a fixed slog.Level, so an operator can flip verbosity on
	// the live logger via SetLevel without a restart. setBaseline records
	// the configured level as the revert target and applies it now.
	ctrl.setBaseline(parseLevel(level))
	opts := &slog.HandlerOptions{
		Level:       ctrl.lv,
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
// every log record. It applies two central, call-site-agnostic
// transforms to string values so no individual logging call has to
// remember either one:
//
//  1. Secret redaction (lookout.Redact) — a stray bearer token, sk-...
//     key, or password=... assignment becomes ***REDACTED:{kind}***.
//  2. Control-character neutralization (CWE-117 / log-injection) — CR,
//     LF, and other C0 controls in an attacker-influenced value are
//     escaped to their two-char forms (\n, \r, \x1b, ...) so a value
//     like "ok\nFAKE-ERROR forged line" can never split one log record
//     into two. This is the single mechanism that defends every
//     slog string attribute in the binary; wiring it here means the 400+
//     user-derived log call sites CodeQL flags as go/log-injection are
//     neutralized centrally rather than patched one by one.
//
// The built-in time/level/source keys keep their stable shapes and are
// returned untouched. The message key is exempt from *redaction* (its
// values are developer-authored format strings, and rewriting them
// risks breaking downstream parsers) but IS run through control-char
// neutralization: a forged newline in the msg is just as much an
// injection vector as one in an attr, and a legitimate message never
// contains a raw newline.
func redactAttr(groups []string, a slog.Attr) slog.Attr {
	_ = groups
	switch a.Key {
	case slog.TimeKey, slog.LevelKey, slog.SourceKey:
		return a
	case slog.MessageKey:
		if a.Value.Kind() != slog.KindString {
			return a
		}
		if neutral := neutralizeControl(a.Value.String()); neutral != a.Value.String() {
			a.Value = slog.StringValue(neutral)
		}
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
	// Neutralize AFTER redaction so the secret detector sees the original
	// bytes; the escaped form is only what lands on the wire.
	out := neutralizeControl(redacted)
	if out == s {
		return a
	}
	a.Value = slog.StringValue(out)
	return a
}

// neutralizeControl escapes CR, LF, and every other C0 control character
// (plus DEL) except horizontal tab, replacing each with a printable
// two-char escape so an attacker-controlled substring cannot forge extra
// log lines or smuggle terminal escape sequences. Tab is preserved as a
// benign, commonly-legitimate whitespace character. The fast path
// returns the input unchanged when it holds no forgeable control byte,
// so the overwhelmingly common clean-string case allocates nothing.
func neutralizeControl(s string) string {
	if !strings.ContainsFunc(s, isForgeableControl) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteRune(r)
		case isForgeableControl(r):
			// Any other C0 control or DEL: emit a \xNN escape.
			const hex = "0123456789abcdef"
			b.WriteString(`\x`)
			b.WriteByte(hex[byte(r)>>4])
			b.WriteByte(hex[byte(r)&0x0f])
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isForgeableControl reports whether r is a control character that can be
// abused to forge log structure: the C0 range (U+0000–U+001F) and DEL
// (U+007F), excluding horizontal tab. LF and CR are the primary
// log-injection vectors; the rest are escaped defensively.
func isForgeableControl(r rune) bool {
	return (r < 0x20 && r != '\t') || r == 0x7f
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
