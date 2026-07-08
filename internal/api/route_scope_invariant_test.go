package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// route_scope_invariant_test.go — the enforcement + enumeration tests for CLI
// token scopes (#864). Before this, canScope had ZERO callers: a token issued
// with only "agents:read" carried the full user role on every endpoint — a
// "restricted" CI token that was not restricted at all (fail-open). These
// tests pin the fix: the route-table chokepoint (requireRoleScopeMW) now
// consults canScope, every mutation route declares a scope, and the headline
// acceptance (agents:read → 403 on a write, 200 on an unrelated read) holds
// end-to-end through the real auth stack.

// withTokenScopes attaches a CLI-token scope set to the request context,
// mirroring what AuthMiddleware.RequireAuth does after validating a scoped CLI
// token. Passing no scopes (the zero call) leaves the request as an
// unrestricted / JWT caller — canScope then returns true (legacy full-role).
func withTokenScopes(req *http.Request, scopes ...string) *http.Request {
	set := make(stringSet, len(scopes))
	for _, s := range scopes {
		set[s] = struct{}{}
	}
	return req.WithContext(context.WithValue(req.Context(), ctxTokenScopes, set))
}

// TestEveryMutationRouteDeclaresScope walks the recorded route table and
// asserts every mutation route carries a scope declaration that is either the
// scopeSelf sentinel (ownership-gated, resource scope N/A) or a scope in the
// mintable vocabulary (knownScopes). A new mutation route whose resource is
// not mapped by scopeForRoute records an empty scope and fails here — the same
// "can't ship ungated" guarantee #824 gives for roles, extended to scopes.
func TestEveryMutationRouteDeclaresScope(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	if len(r.mutationRoutes) == 0 {
		t.Fatal("mutationRoutes is empty: the route-table chokepoint is not wired")
	}
	for _, mr := range r.mutationRoutes {
		key := mr.Method + " " + mr.Pattern
		if mr.Scope == scopeSelf {
			continue // ownership-gated, scope-exempt by design
		}
		if mr.Scope == "" {
			t.Errorf("mutation route %q declares no scope — scopeForRoute has no mapping for its resource; add one (or the route must be roleSelf if it is ownership-gated)", key)
			continue
		}
		if _, ok := knownScopes[mr.Scope]; !ok {
			t.Errorf("mutation route %q declares scope %q which is not in the mintable vocabulary (knownScopes) — a token can never hold it, so the route is unreachable for scoped tokens", key, mr.Scope)
		}
	}
}

// TestEveryMintableScopeMapsToARoute is the REVERSE invariant of
// TestEveryMutationRouteDeclaresScope: it asserts the vocabulary carries no
// dead weight — every scope a user can MINT (knownScopes) is satisfiable
// against at least one recorded mutation route. A mintable scope that maps to
// no route is a lie: an operator scopes a token to it expecting least
// privilege, but the token can never exercise that grant (and a read-shaped
// scope silently gates nothing, because reads are not scope-gated yet). The
// forward invariant only checks routes→vocabulary; without this reverse check,
// a scope like agents:run or crews:read can sit in the New Token dialog,
// validate at issue time, and do absolutely nothing — which is exactly the gap
// that shipped past green CI.
func TestEveryMintableScopeMapsToARoute(t *testing.T) {
	r, err := NewRouter(setupTestDB(t), "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	satisfiesSomeRoute := func(scope string) bool {
		// A token holding exactly {scope}: does it satisfy any route's required
		// scope (directly or via wildcard subsumption)?
		ctx := context.WithValue(context.Background(), ctxTokenScopes, stringSet{scope: {}})
		for _, mr := range r.mutationRoutes {
			if mr.Scope == scopeSelf || mr.Scope == "" {
				continue
			}
			if canScope(ctx, mr.Scope) {
				return true
			}
		}
		return false
	}
	for scope := range knownScopes {
		if !satisfiesSomeRoute(scope) {
			t.Errorf("mintable scope %q maps to no mutation route — a token scoped to it can exercise nothing. Remove it from knownScopes (and the New Token dialog + auth.mdx) until a route requires it, or wire the route that should require it.", scope)
		}
	}
}

// TestScopeForRoute pins the resource→scope mapping: the five first-class
// families resolve to their own scope, nested sub-resources borrow the target
// resource's scope, the broad workspace surface maps to workspace:admin, and
// an unmapped resource fails closed ("").
func TestScopeForRoute(t *testing.T) {
	cases := []struct {
		pattern string
		want    string
	}{
		{"/api/v1/agents", "agents:write"},
		{"/api/v1/agents/{agentId}/persona", "agents:write"},
		{"/api/v1/agents/{agentId}/credentials", "credentials:write"}, // sub-resource borrows scope
		{"/api/v1/agents/{agentId}/skills", "skills:write"},           // sub-resource borrows scope
		{"/api/v1/crews", "crews:write"},
		{"/api/v1/crews/{crewId}/missions", "crews:write"},
		{"/api/v1/crew-connections", "crews:write"},
		{"/api/v1/credentials", "credentials:write"},
		{"/api/v1/credential-rotations/{rotationId}", "credentials:write"},
		{"/api/v1/workspaces/{workspaceId}/skills/import", "skills:write"},
		{"/api/v1/notification-channels", "webhooks:write"},
		{"/api/v1/workspaces/{workspaceId}/pipeline-webhooks", "webhooks:write"},
		{"/api/v1/workspaces/{workspaceId}", "workspace:admin"},
		{"/api/v1/workspaces/{workspaceId}/pipelines/{slug}/run", "workspace:admin"},
		{"/api/v1/admin/backups", "workspace:admin"},
		{"/api/v1/projects", "workspace:admin"},
		{"/api/v1/feature-flags", "workspace:admin"},
		{"/api/v1/totally-unknown-resource", ""}, // fail-closed
		{"/api/v1/", ""},
	}
	for _, c := range cases {
		if got := scopeForRoute(c.pattern); got != c.want {
			t.Errorf("scopeForRoute(%q) = %q, want %q", c.pattern, got, c.want)
		}
		// Every non-empty resolved scope must be mintable, else scoped tokens
		// can never satisfy it.
		if c.want != "" {
			if _, ok := knownScopes[c.want]; !ok {
				t.Errorf("scopeForRoute(%q) resolved to %q which is not in knownScopes", c.pattern, c.want)
			}
		}
	}
}

// TestRequireScopeMW_Enforcement proves the chokepoint consults canScope: a
// scoped token missing the route's scope is 403'd before the handler runs;
// exact / resource-wildcard / global-wildcard scopes pass; an unscoped (legacy
// or JWT) caller always passes; and scopeSelf routes are exempt. Role is held
// at roleInline so the role gate passes through and the scope gate is isolated.
func TestRequireScopeMW_Enforcement(t *testing.T) {
	r := &Router{}
	reached := "REACHED"
	h := func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(reached)) }

	cases := []struct {
		name        string
		routeScope  string
		tokenScopes []string // nil = unrestricted (legacy / JWT)
		wantReach   bool
	}{
		{"legacy unscoped token passes (full role)", "crews:write", nil, true},
		{"exact scope match", "crews:write", []string{"crews:write"}, true},
		{"resource wildcard subsumes", "crews:write", []string{"crews:*"}, true},
		{"global wildcard subsumes", "crews:write", []string{"*"}, true},
		{"wrong resource denied", "crews:write", []string{"agents:read"}, false},
		{"read scope denied on write route", "agents:write", []string{"agents:read"}, false},
		{"explicit-empty scope set denied", "crews:write", []string{}, false},
		{"scopeSelf exempt even for a scoped token", scopeSelf, []string{"agents:read"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mw := r.requireRoleScopeMW(roleInline, c.routeScope, h)
			rr := httptest.NewRecorder()
			req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), "u1", "w1", "OWNER")
			if c.tokenScopes != nil {
				req = withTokenScopes(req, c.tokenScopes...)
			}
			mw.ServeHTTP(rr, req)
			reachedHandler := rr.Body.String() == reached
			if reachedHandler != c.wantReach {
				t.Fatalf("routeScope=%s tokenScopes=%v: reached=%v (code=%d), want reach=%v",
					c.routeScope, c.tokenScopes, reachedHandler, rr.Code, c.wantReach)
			}
			if !c.wantReach && rr.Code != http.StatusForbidden {
				t.Fatalf("routeScope=%s tokenScopes=%v: code=%d, want 403", c.routeScope, c.tokenScopes, rr.Code)
			}
		})
	}
}

// TestTokenScopeEnforcement_EndToEnd is the acceptance test from the issue: a
// real CLI token scoped to only "agents:read", authenticated through the full
// router stack, is 403'd on POST /api/v1/crews (a crews:write mutation) yet
// still reads GET /api/v1/agents (a read, not scope-gated in this pass). The
// legacy unscoped token and the wildcard token are pinned as the two ends of
// the compatibility spectrum. The user is OWNER, so role never denies — the
// only thing that can 403 the write is the scope gate.
func TestTokenScopeEnforcement_EndToEnd(t *testing.T) {
	mkRouter := func(t *testing.T, scopesJSON any) (*Router, string) {
		t.Helper()
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		seedTestWorkspace(t, db, userID)
		plaintext := "crewship_cli_aabbccdd11223344556677889900"
		if _, err := db.Exec(
			`INSERT INTO cli_tokens (id, user_id, name, token_hash, scopes, created_at) VALUES (?, ?, ?, ?, ?, datetime('now'))`,
			"clt-scope", userID, "test-cli", sha256Hex(plaintext), scopesJSON,
		); err != nil {
			t.Fatalf("seed cli token: %v", err)
		}
		r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
		if err != nil {
			t.Fatalf("NewRouter: %v", err)
		}
		return r, plaintext
	}

	do := func(r *Router, token, method, path string) int {
		req := httptest.NewRequest(method, path+"?workspace_id=test-workspace-id", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Code
	}

	t.Run("agents:read token is denied a crews:write mutation but can read agents", func(t *testing.T) {
		r, token := mkRouter(t, `["agents:read"]`)
		if code := do(r, token, "POST", "/api/v1/crews"); code != http.StatusForbidden {
			t.Errorf("POST /api/v1/crews with agents:read token: got %d, want 403", code)
		}
		if code := do(r, token, "GET", "/api/v1/agents"); code != http.StatusOK {
			t.Errorf("GET /api/v1/agents with agents:read token: got %d, want 200", code)
		}
	})

	t.Run("legacy unscoped token keeps full role (crews:write allowed past scope gate)", func(t *testing.T) {
		r, token := mkRouter(t, nil) // NULL scopes column = unrestricted
		if code := do(r, token, "POST", "/api/v1/crews"); code == http.StatusForbidden {
			t.Errorf("POST /api/v1/crews with legacy unscoped token: got 403, want the scope gate to pass (any non-403)")
		}
	})

	t.Run("wildcard token passes the scope gate", func(t *testing.T) {
		r, token := mkRouter(t, `["*"]`)
		if code := do(r, token, "POST", "/api/v1/crews"); code == http.StatusForbidden {
			t.Errorf("POST /api/v1/crews with * token: got 403, want the scope gate to pass (any non-403)")
		}
	})

	t.Run("crews:write token passes the scope gate for the crews mutation", func(t *testing.T) {
		r, token := mkRouter(t, `["crews:write"]`)
		if code := do(r, token, "POST", "/api/v1/crews"); code == http.StatusForbidden {
			t.Errorf("POST /api/v1/crews with crews:write token: got 403, want the scope gate to pass (any non-403)")
		}
	})
}
