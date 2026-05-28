// Routes-registration smoke test for the Connectors API surface.
//
// The four routes are wired in registerCrewsRoutes (router_crews.go),
// adjacent to /api/v1/integrations and /api/v1/recipes. This file
// covers:
//
//  1. NewConnectorHandler can be constructed against the test DB
//     without panicking on the embedded fixture set.
//  2. The four exported handler methods (List, Get, Verify, Install)
//     accept the request shape produced by Go's net/http ServeMux
//     with `{connectorId}` path parameters.
//  3. The routes are actually registered on a real Router — a regression
//     guard so the wiring can't silently disappear (see
//     TestConnectorRoutes_WiredIntoRouter).
package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
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

// TestConnectorRoutes_WiredIntoRouter builds a real Router and confirms
// GET /api/v1/connectors is actually registered — i.e. an authed request
// does NOT 404. This is the regression guard for the route wiring in
// router_crews.go: if someone deletes the r.mux.Handle lines, the catalog
// browse endpoint would start 404ing and this test fails.
func TestConnectorRoutes_WiredIntoRouter(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)

	const secret = "test-secret-for-jwt-signing-32chars!!"
	r, err := NewRouter(db, secret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	v, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatalf("auth.NewJWTValidator: %v", err)
	}
	sess, err := sessions.NewDBStore(db).Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, "Test User", "test@example.com")
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Fatalf("GET /api/v1/connectors returned 404 — route not wired into the Router; body: %s", rr.Body.String())
	}
	if rr.Code != http.StatusOK {
		t.Errorf("GET /api/v1/connectors = %d, want 200 (catalog browse needs only auth); body: %s", rr.Code, rr.Body.String())
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
