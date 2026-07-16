package composio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// APIError is a non-2xx response from the Composio API. It carries the
// upstream status and a bounded body snippet so callers can classify the
// failure (invalid key vs bad request vs Composio outage) instead of
// collapsing everything into an opaque gateway error (#1192). Transport-level
// failures (DNS, dial, TLS, timeout) are NOT APIErrors — they stay wrapped
// *url.Error values; describe those with TransportReason.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Snippet    string // trimmed, size-capped raw body excerpt (server-log grade)
}

// Error keeps the historical "composio: <method> <path>: status <code>: <body>"
// shape. CreateMCPServer's invalid-tools retry matches substrings of this
// (error code + rejected tool slugs), so the body snippet must stay verbatim.
func (e *APIError) Error() string {
	return fmt.Sprintf("composio: %s %s: status %d: %s", e.Method, e.Path, e.StatusCode, e.Snippet)
}

// detailMaxRunes bounds what Detail returns: enough to carry Composio's error
// message, small enough that an arbitrary upstream body can't flood an API
// response the CLI prints verbatim.
const detailMaxRunes = 200

// Detail returns a short, single-line, human-readable reason suitable for an
// operator-facing error payload. It prefers the message field of Composio's
// JSON error envelope ({"error":{"message":…}}, {"error":"…"} or
// {"message":"…"}) and falls back to a sanitized, truncated excerpt of the
// raw body. Never returns credentials: Composio error bodies don't echo the
// API key, and the excerpt is bounded regardless.
func (e *APIError) Detail() string {
	msg := extractUpstreamMessage(e.Snippet)
	if msg == "" {
		msg = e.Snippet
	}
	return sanitizeDetail(msg)
}

// extractUpstreamMessage pulls the human message out of Composio's JSON error
// envelope, returning "" when the body isn't JSON or has no message field.
func extractUpstreamMessage(body string) string {
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(body), &env) != nil {
		return ""
	}
	if len(env.Error) > 0 {
		var s string
		if json.Unmarshal(env.Error, &s) == nil && s != "" {
			return s
		}
		var obj struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(env.Error, &obj) == nil && obj.Message != "" {
			return obj.Message
		}
	}
	return env.Message
}

// sanitizeDetail collapses control characters to spaces, squeezes runs of
// whitespace, trims, and caps the result at detailMaxRunes (appending an
// ellipsis when truncated) so it is safe to embed in a one-line problem
// detail.
func sanitizeDetail(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if !lastSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			lastSpace = true
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	out := strings.TrimSpace(b.String())
	runes := []rune(out)
	if len(runes) > detailMaxRunes {
		out = string(runes[:detailMaxRunes]) + "…"
	}
	return out
}

// TransportReason describes a connection-level client failure (DNS, dial,
// TLS, timeout) without echoing the full request URL — query strings carry
// caller-supplied filters that don't belong in an error payload. Returns a
// sanitized, bounded string; "" for a nil error.
func TransportReason(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "request timed out"
		}
		if ue.Err != nil {
			return sanitizeDetail(ue.Err.Error())
		}
	}
	// Fall back to the innermost wrapped error (drops our "composio: GET
	// /path:" prefixes without dumping the URL).
	inner := err
	for {
		next := errors.Unwrap(inner)
		if next == nil {
			break
		}
		inner = next
	}
	return sanitizeDetail(inner.Error())
}
