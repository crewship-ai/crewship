package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file implements finding T0.2 from the 2026-06 security audit
// (.claude/context/SECURITY-AUDIT-2026-06.md, Tier 0 invariant sweep):
//
//	"Unauthenticated reachability sweep — every non-allowlisted /api/v1/*
//	 route → 401 without creds. Allowlist = health, setup-status, telemetry,
//	 bootstrap, auth/*, oauth/callback, webhooks/*, /exposed/*."
//
// The auth posture is currently SOUND here (every sensitive surface is
// wrapped by AuthMiddleware.RequireAuth via the `authed(...)` helper in the
// route registrars), so these are REGRESSION GUARDS, not tripwires: they
// pass today and flip to FAIL the moment a future PR mounts a sensitive
// route without the `authed(...)` wrapper, or accidentally strips the auth
// requirement off an existing one.
//
// Mechanism: build the real Router (NewRouter runs the full registerRoutes
// chain) and dispatch each request through r.mux directly. Going through the
// mux exercises the per-route middleware stack — crucially the RequireAuth
// wrapper — while bypassing the outer per-IP rate limiter so a large table
// can't make the sweep flaky by tripping a 429 mid-run. RequireAuth runs
// before the workspace-context middleware, so an unauthenticated request to
// a workspace-scoped route returns 401 (no credentials), never 400
// (missing workspace_id).

// testRouterSecret is the >=32-char JWT secret the package's other router
// tests use; NewJWTValidator enforces a minimum length.
const testRouterSecret = "this-is-a-32-char-test-secret-pad"

// newReachabilityRouter constructs the production router with the full route
// set registered. No auth-related options are passed: we want the default,
// real middleware wiring.
func newReachabilityRouter(t *testing.T) *Router {
	t.Helper()
	r, err := NewRouter(setupTestDB(t), testRouterSecret, newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	t.Cleanup(r.Shutdown)
	return r
}

// route is one (method, path) pair to probe with no credentials.
type route struct {
	method string
	path   string
}

// sensitiveRoutes is a representative cross-section of non-allowlisted
// /api/v1/* surfaces — every cluster the audit calls out (agents, crews,
// credentials, admin, backups, instance settings, feature flags, missions,
// issues, runs, integrations, oauth-authenticated, sessions/CLI tokens) plus
// read/mutate/delete verbs. Each MUST return 401 without a token. This is a
// sample, not an exhaustive enumeration: Go's ServeMux exposes no public API
// to iterate registered patterns, and the audit explicitly accepts "a
// representative set of sensitive routes" when full enumeration is
// impractical. Add a row here whenever a new sensitive surface lands.
var sensitiveRoutes = []route{
	// Workspaces / crews
	{"GET", "/api/v1/workspaces"},
	{"POST", "/api/v1/workspaces"},
	{"GET", "/api/v1/workspaces/ws-1"},
	{"PATCH", "/api/v1/workspaces/ws-1"},
	{"GET", "/api/v1/workspaces/ws-1/members"},
	{"POST", "/api/v1/workspaces/ws-1/members"},
	{"DELETE", "/api/v1/workspaces/ws-1/members/m-1"},
	{"GET", "/api/v1/crews"},
	{"POST", "/api/v1/crews"},
	{"GET", "/api/v1/crews/crew-1"},
	{"PATCH", "/api/v1/crews/crew-1"},
	{"DELETE", "/api/v1/crews/crew-1"},
	{"POST", "/api/v1/crews/crew-1/members"},

	// Agents (read / mutate / control-plane)
	{"GET", "/api/v1/agents"},
	{"POST", "/api/v1/agents"},
	{"POST", "/api/v1/agents/hire"},
	{"GET", "/api/v1/agents/agent-1"},
	{"PATCH", "/api/v1/agents/agent-1"},
	{"DELETE", "/api/v1/agents/agent-1"},
	{"POST", "/api/v1/agents/agent-1/stop"},
	{"GET", "/api/v1/agents/agent-1/logs"},
	{"GET", "/api/v1/agents/agent-1/files"},

	// Credentials — privilege-sensitive, secret-bearing
	{"GET", "/api/v1/credentials"},
	{"POST", "/api/v1/credentials"},
	{"POST", "/api/v1/credentials/test"},
	{"POST", "/api/v1/credentials/cred-1/rotate"},
	{"GET", "/api/v1/credentials/cred-1/audit"},

	// OAuth (the authenticated arms — callback is allowlisted separately)
	{"GET", "/api/v1/oauth/providers"},
	{"POST", "/api/v1/oauth/initiate"},
	{"POST", "/api/v1/oauth/exchange"},

	// Integrations
	{"GET", "/api/v1/integrations"},
	{"POST", "/api/v1/integrations"},

	// Missions / issues / runs
	{"GET", "/api/v1/missions"},
	{"GET", "/api/v1/issues"},
	{"GET", "/api/v1/runs"},

	// Admin surface
	{"GET", "/api/v1/admin/stats"},
	{"GET", "/api/v1/admin/users"},
	{"GET", "/api/v1/admin/workspaces"},

	// Backups — full DR control plane
	{"GET", "/api/v1/admin/backups"},
	{"POST", "/api/v1/admin/backups"},
	{"DELETE", "/api/v1/admin/backups"},
	{"POST", "/api/v1/admin/backups/restore"},
	{"GET", "/api/v1/admin/backups/download"},

	// Instance settings + feature flags (multi-tenant-global)
	{"GET", "/api/v1/instance/settings"},
	{"PUT", "/api/v1/instance/settings/some-key"},
	{"GET", "/api/v1/feature-flags"},
	{"POST", "/api/v1/feature-flags"},

	// Session / CLI-token management
	{"GET", "/api/v1/auth/sessions"},
	{"GET", "/api/v1/auth/cli-tokens"},
	{"GET", "/api/v1/ws-token"},

	// System (the authenticated arms)
	{"GET", "/api/v1/system/runtime"},
	{"GET", "/api/v1/system/keeper"},
}

func TestUnauthenticatedReachability_SensitiveRoutesReturn401(t *testing.T) {
	r := newReachabilityRouter(t)

	for _, rt := range sensitiveRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			// No Authorization header, no session cookie — a fully
			// anonymous caller. Body is nil; RequireAuth rejects before
			// any handler ever decodes it.
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rr := httptest.NewRecorder()
			r.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("T0.2 REGRESSION: %s %s returned %d without credentials, want 401 — "+
					"a sensitive route was mounted without the authed(...) wrapper (or had it stripped). body=%q",
					rt.method, rt.path, rr.Code, rr.Body.String())
			}
		})
	}
	t.Logf("T0.2 guard: %d sensitive routes all reject anonymous callers with 401", len(sensitiveRoutes))
}

// allowlistedRoutes are the intentionally-unauthenticated surfaces named in
// T0.2's allowlist. Probing them anonymously must NOT yield 401 — they are
// reachable by design (health, the pre-auth setup/telemetry probes, the
// bootstrap/signup bootstrap flow, the NextAuth handshake, and the OAuth
// callback which authenticates via a state token rather than a session). We
// assert "not 401" rather than a specific success code because several of
// these legitimately return 200/400/405 depending on their (missing) input;
// the only property under test is that the auth gate does NOT fire.
var allowlistedRoutes = []route{
	{"GET", "/api/health"},
	{"GET", "/api/v1/system/setup-status"},
	{"GET", "/api/v1/system/telemetry"},
	{"POST", "/api/v1/bootstrap"},
	{"POST", "/api/v1/auth/signup"},
	{"POST", "/api/v1/auth/forgot"},
	{"GET", "/api/auth/csrf"},
	{"GET", "/api/auth/session"},
	{"GET", "/api/v1/oauth/callback"},
}

func TestUnauthenticatedReachability_AllowlistNotGatedByAuth(t *testing.T) {
	r := newReachabilityRouter(t)

	for _, rt := range allowlistedRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			rr := httptest.NewRecorder()
			r.mux.ServeHTTP(rr, req)

			if rr.Code == http.StatusUnauthorized {
				t.Fatalf("allowlisted route %s %s returned 401 — it must be reachable without "+
					"credentials (T0.2 allowlist); an auth wrapper was added by mistake. body=%q",
					rt.method, rt.path, rr.Body.String())
			}
		})
	}
}

// TestUnauthenticatedReachability_BearerGarbageStillRejected confirms the gate
// rejects a *present but invalid* token too, not merely a missing one — a
// missing-credentials-only check would let a forged/expired bearer through to
// the handler. Covers both the JWT and CLI-token (csk_ prefix) arms of
// extractToken/RequireAuth.
func TestUnauthenticatedReachability_BearerGarbageStillRejected(t *testing.T) {
	r := newReachabilityRouter(t)

	tokens := map[string]string{
		"garbage-jwt":      "not-a-real-jwt",
		"empty-bearer":     "",
		"cli-token-shaped": "csk_deadbeefdeadbeefdeadbeefdeadbeef",
	}

	for name, tok := range tokens {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/agents", nil)
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rr := httptest.NewRecorder()
			r.mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("GET /api/v1/agents with %s token = %d, want 401; body=%q",
					name, rr.Code, rr.Body.String())
			}
		})
	}
}
