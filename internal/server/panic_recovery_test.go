package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newQuietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPanicRecoveryMiddleware_RecoversAndReturns500(t *testing.T) {
	t.Parallel()

	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	wrapped := panicRecoveryMiddleware(newQuietLogger(), panicker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)

	// Must NOT propagate the panic; the recover() inside the middleware
	// is exactly the contract under test.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic leaked past middleware: %v", r)
		}
	}()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: want 500, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Errorf("body should contain 'internal server error', got %q", rec.Body.String())
	}
}

func TestPanicRecoveryMiddleware_PassThroughOnNormalResponse(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewing"))
	})
	wrapped := panicRecoveryMiddleware(newQuietLogger(), ok)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/normal", nil)
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("status: want 418, got %d", rec.Code)
	}
	if rec.Body.String() != "brewing" {
		t.Errorf("body: want 'brewing', got %q", rec.Body.String())
	}
}

func TestPanicRecoveryMiddleware_ReraisesAbortHandler(t *testing.T) {
	t.Parallel()

	panicker := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	wrapped := panicRecoveryMiddleware(newQuietLogger(), panicker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/abort", nil)

	// http.ErrAbortHandler MUST propagate so net/http can do its
	// silent-abort handling. If our middleware swallowed it, we'd
	// turn legitimate client disconnects into bogus 500s.
	var caught any
	func() {
		defer func() { caught = recover() }()
		wrapped.ServeHTTP(rec, req)
	}()
	if caught != http.ErrAbortHandler {
		t.Fatalf("expected ErrAbortHandler to propagate, got %v", caught)
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		path    string
		upgrade string
		want    bool
	}{
		{"plain api", "/api/v1/foo", "", false},
		{"ws path", "/ws", "", true},
		{"ws terminal path", "/ws/terminal", "", true},
		{"upgrade header lowercase", "/api/v1/foo", "websocket", true},
		// RFC 6455 mandates case-insensitive — exact-equality used to miss
		// these and let WS panics fall through to the 500 writer, which
		// then corrupted the in-flight upgrade response.
		{"upgrade header mixed case", "/api/v1/foo", "WebSocket", true},
		{"upgrade header upper", "/api/v1/foo", "WEBSOCKET", true},
		// RFC 9110 allows comma-separated protocol lists.
		{"upgrade comma list", "/api/v1/foo", "h2c, websocket", true},
		{"upgrade with spaces", "/api/v1/foo", "  websocket  ", true},
		{"unknown upgrade header", "/api/v1/foo", "h2c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.upgrade != "" {
				req.Header.Set("Upgrade", tc.upgrade)
			}
			if got := isWebSocketUpgrade(req); got != tc.want {
				t.Errorf("isWebSocketUpgrade(%s, %s): want %v, got %v", tc.path, tc.upgrade, tc.want, got)
			}
		})
	}
}
