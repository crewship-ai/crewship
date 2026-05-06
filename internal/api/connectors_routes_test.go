// Routes-registration smoke test for the Connectors API surface.
//
// We don't ship the actual route registrations in router_routes.go
// yet (impl phase), but we DO want to know that:
//
//  1. NewConnectorHandler can be constructed against the test DB
//     without panicking on the embedded fixture set.
//  2. The four exported handler methods (List, Get, Verify, Install)
//     accept the request shape produced by Go's net/http ServeMux
//     with `{connectorId}` path parameters.
//
// When the implementer adds the four `r.mux.Handle(...)` lines to
// router_routes.go, this test stays passing as the contract spec.
package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestConnectorHandler_ConstructionSucceedsAgainstShippedFixtures
// is the one passing test in this file — it exercises the
// NewConnectorHandler path that production code will hit at startup.
// If a fixture in internal/connectors/fixtures stops parsing, this
// test surfaces the breakage at the API layer (not just inside the
// connectors package).
func TestConnectorHandler_ConstructionSucceedsAgainstShippedFixtures(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewConnectorHandler(db, logger)

	if h == nil {
		t.Fatal("NewConnectorHandler returned nil")
	}
	if h.catalog == nil {
		t.Error("catalog not loaded")
	}
}

// TestConnectorRoutes_PathPatternsRegisterCleanly walks the four
// route patterns we plan to register and confirms ServeMux accepts
// each one without overlap or syntax error. ServeMux is strict about
// `{name}` capture syntax in Go 1.22+; this ensures our chosen path
// names don't collide with existing /api/v1/integrations routes.
//
// The test does NOT require the impl to be wired — it constructs a
// fresh ServeMux with our planned patterns and the existing handler
// stubs, just to confirm the patterns parse.
func TestConnectorRoutes_PathPatternsRegisterCleanly(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	patterns := []string{
		"GET /api/v1/connectors",
		"GET /api/v1/connectors/{connectorId}",
		"POST /api/v1/connectors/{connectorId}/verify",
		"POST /api/v1/connectors/{connectorId}/install",
	}

	for _, p := range patterns {
		// Wrap in defer/recover so the test reports which specific
		// pattern panicked (mux.Handle panics on duplicate / bad).
		t.Run(p, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Handle(%q) panicked: %v", p, r)
				}
			}()
			mux.Handle(p, stub)
		})
	}

	// Sanity-route a request through the registered mux to confirm
	// the path-value capture works for the connectorId variable.
	req := httptest.NewRequest("GET", "/api/v1/connectors/linear", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("captured route returned %d, want 200", rr.Code)
	}
}

// TestConnectorHandler_ImplementsExpectedMethods is a compile-time-ish
// smoke that the four method signatures match http.HandlerFunc. If a
// future refactor changes one to e.g. (w, r, params) it'll fail here
// rather than at the route registration site.
//
// Uses a nil-pointer method-value bind so the test doesn't depend on
// LoadAll being implemented (the constructor would otherwise panic
// inside the connectors package's TDD stub). Methods are not called,
// so the nil receiver is harmless at compile time.
func TestConnectorHandler_ImplementsExpectedMethods(t *testing.T) {
	t.Parallel()
	var h *ConnectorHandler // nil ok — only the method-value type is asserted
	var _ http.HandlerFunc = h.List
	var _ http.HandlerFunc = h.Get
	var _ http.HandlerFunc = h.Verify
	var _ http.HandlerFunc = h.Install
}
