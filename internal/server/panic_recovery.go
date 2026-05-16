package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/crewship-ai/crewship/internal/crashreport"
)

// panicRecoveryMiddleware wraps the entire HTTP handler chain in a top-level
// recover() so a single nil-deref or assertion failure in any handler returns
// 500 to the client instead of taking down the whole process.
//
// Go's http.Server does NOT swallow handler panics by default — it logs the
// stack and aborts the connection, but any goroutines the handler launched
// before panicking will keep their refs live (db rows, channels), and a panic
// in a hot path can starve the listener of usable workers. The recover here
// is the last line of defence; individual handlers should still fail
// gracefully on their own.
//
// Panics are forwarded to crashreport (Sentry when consent + DSN are present;
// no-op otherwise) so the maintainer gets a signal without depending on
// users to file logs by hand. WebSocket upgrade paths (/ws, /ws/terminal)
// are excluded because once Hijack() runs, the ResponseWriter contract no
// longer guarantees Write() works — emitting an HTTP 500 there could
// corrupt the in-flight frame stream.
func panicRecoveryMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the idiomatic signal that a handler
			// wants to abort silently (e.g. client disconnect, hijack
			// teardown). Re-panic so net/http handles it normally.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			stack := debug.Stack()
			err := fmt.Errorf("panic in HTTP handler %s %s: %v", r.Method, r.URL.Path, rec)
			logger.Error("HTTP handler panic recovered",
				"method", r.Method,
				"path", r.URL.Path,
				"panic", rec,
				"stack", string(stack),
			)
			crashreport.Capture(err, map[string]string{
				"surface": "http_handler",
				"method":  r.Method,
				"path":    r.URL.Path,
			})
			if isWebSocketUpgrade(r) {
				return
			}
			// Best-effort 500. If headers were already flushed by the
			// handler before panicking, WriteHeader is a no-op and the
			// client sees a half-written response — Go logs that. We
			// can't do better without buffering every response, which
			// would break SSE / streaming endpoints.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("internal server error\n"))
		}()
		next.ServeHTTP(w, r)
	})
}

func isWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.URL.Path == "/ws" || r.URL.Path == "/ws/terminal" {
		return true
	}
	for _, v := range r.Header.Values("Upgrade") {
		if v == "websocket" {
			return true
		}
	}
	return false
}
