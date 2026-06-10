package api

// PR-F24 — X-Internal-Token workspace binding.
//
// These tests pin the close of the documented symmetric cross-tenant
// bypass: pre-fix the internal token was one global secret and
// internalWsCtx trusted ?workspace_id, so a caller holding the token
// (e.g. an agent that captured it inside its container) could aim any
// internal route at any workspace. Post-fix sidecars hold a
// workspace-bound derived token; requireInternal validates the
// binding and refuses any request whose ?workspace_id disagrees with
// the workspace baked into the token.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const bindTestMaster = "master-secret-0123456789abcdef"

func bindTestRequest(token, query string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/x"+query, nil)
	req.RemoteAddr = "127.0.0.1:1234" // pass the network gate
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	return req
}

// TestRequireInternal_WorkspaceBoundToken_AuthorizesOwnWorkspace is
// the compatibility half: the derived token a sidecar receives at
// startup must authorize requests for its own workspace (with or
// without the ?workspace_id query parameter the sidecar attaches).
//
// PR-F24 hardening: the "without_workspace_query" case no longer pins
// the hole (bound token, no query → 200 with UNSCOPED reach). It pins
// the new mandatory-scope semantics instead: requireInternal INJECTS
// the bound workspace into the request query so every handler that
// filters by ?workspace_id is tenant-scoped automatically, with no
// "legacy unscoped" fall-through for bound tokens.
func TestRequireInternal_WorkspaceBoundToken_AuthorizesOwnWorkspace(t *testing.T) {
	t.Parallel()
	h := NewInternalHandler(nil, bindTestMaster, testLogger())
	tok := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"with_matching_workspace_query", "?workspace_id=ws_a"},
		{"without_workspace_query", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sawBound, sawQueryWS string
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawBound = InternalTokenWorkspaceFromContext(r.Context())
				sawQueryWS = r.URL.Query().Get("workspace_id")
				w.WriteHeader(http.StatusOK)
			})
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, bindTestRequest(tok, tc.query))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
			}
			if sawBound != "ws_a" {
				t.Errorf("downstream saw bound workspace %q, want ws_a", sawBound)
			}
			// The mandatory-scope guarantee: whether or not the caller
			// supplied the query, downstream MUST observe the bound
			// workspace projected onto ?workspace_id — so a query-scoped
			// handler (webhook secret, list credentials, …) can never run
			// unscoped under a bound token.
			if sawQueryWS != "ws_a" {
				t.Errorf("downstream query workspace_id = %q, want ws_a "+
					"(bound token must inject the scope, never fall through unscoped)", sawQueryWS)
			}
		})
	}
}

// TestRequireInternal_WorkspaceBoundToken_ForgedWorkspace403 is THE
// close: a token bound to workspace A presented with
// ?workspace_id=B must be refused before any handler runs.
func TestRequireInternal_WorkspaceBoundToken_ForgedWorkspace403(t *testing.T) {
	t.Parallel()
	h := NewInternalHandler(nil, bindTestMaster, testLogger())
	tok := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")

	reached := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	h.requireInternal(downstream).ServeHTTP(rr, bindTestRequest(tok, "?workspace_id=ws_victim"))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (cross-workspace request must be refused)", rr.Code)
	}
	if reached {
		t.Error("downstream handler ran despite workspace mismatch")
	}
}

// TestRequireInternal_WorkspaceBoundToken_RejectsForgeries covers the
// token-integrity half of the binding.
func TestRequireInternal_WorkspaceBoundToken_RejectsForgeries(t *testing.T) {
	t.Parallel()
	h := NewInternalHandler(nil, bindTestMaster, testLogger())
	valid := internaltoken.DeriveWorkspaceToken(bindTestMaster, "ws_a")

	cases := []struct {
		name  string
		token string
		query string
	}{
		// Swap the workspace segment but keep the ws_a MAC: classic
		// "rebind my token to the victim tenant" forgery.
		{"swapped_workspace_segment",
			"wsv1.ws_victim." + valid[len("wsv1.ws_a."):], "?workspace_id=ws_victim"},
		{"tampered_mac", valid[:len(valid)-1] + "0", "?workspace_id=ws_a"},
		{"derived_from_wrong_master",
			internaltoken.DeriveWorkspaceToken("some-other-master", "ws_a"), "?workspace_id=ws_a"},
		{"empty_workspace_segment", "wsv1..deadbeef", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("downstream handler ran for a forged token")
			})
			rr := httptest.NewRecorder()
			h.requireInternal(downstream).ServeHTTP(rr, bindTestRequest(tc.token, tc.query))
			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rr.Code)
			}
		})
	}
}

// TestRequireInternal_MasterTokenStillAuthorizes pins single-boot
// compatibility: host-side trusted callers (chatbridge resolver,
// llmproxy monitor) keep using the master token over loopback and
// must continue to work, including with arbitrary ?workspace_id —
// the master is not bound to a workspace by design (it never enters
// a container).
func TestRequireInternal_MasterTokenStillAuthorizes(t *testing.T) {
	t.Parallel()
	h := NewInternalHandler(nil, bindTestMaster, testLogger())
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := InternalTokenWorkspaceFromContext(r.Context()); got != "" {
			t.Errorf("master token must not bind a workspace; got %q", got)
		}
		w.WriteHeader(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	h.requireInternal(downstream).ServeHTTP(rr, bindTestRequest(bindTestMaster, "?workspace_id=ws_any"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (master token is the host-side compat path)", rr.Code)
	}
}

// TestInternalWsCtx_EnforcesTokenBinding pins the defense-in-depth
// re-check inside internalWsCtx: even if requireInternal's own
// mismatch gate were bypassed by a future middleware-chain change,
// internalWsCtx must refuse a query workspace that disagrees with the
// token-bound workspace it finds in context.
func TestInternalWsCtx_EnforcesTokenBinding(t *testing.T) {
	t.Parallel()

	t.Run("mismatch_403", func(t *testing.T) {
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Error("downstream ran despite binding mismatch")
		})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id=ws_victim", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, "ws_a"))
		rr := httptest.NewRecorder()
		internalWsCtx(downstream).ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("match_passes_and_sets_ctx", func(t *testing.T) {
		var sawWS string
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawWS = WorkspaceIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id=ws_a", nil)
		req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, "ws_a"))
		rr := httptest.NewRecorder()
		internalWsCtx(downstream).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if sawWS != "ws_a" {
			t.Errorf("ctx workspace = %q, want ws_a", sawWS)
		}
	})

	t.Run("no_binding_keeps_legacy_behavior", func(t *testing.T) {
		// Master-token callers have no binding in context; the query
		// value flows through as before.
		var sawWS string
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawWS = WorkspaceIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(http.MethodPost, "/x?workspace_id=ws_a", nil)
		rr := httptest.NewRecorder()
		internalWsCtx(downstream).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || sawWS != "ws_a" {
			t.Fatalf("status = %d, ctx ws = %q; want 200/ws_a", rr.Code, sawWS)
		}
	})
}

// TestAssertInternalTokenWorkspace covers the body-workspace guard:
// requireInternal can only see query parameters, so handlers that
// scope by a workspace_id carried in the JSON body (cost record,
// journal emit, pipeline save) call this helper after decoding.
func TestAssertInternalTokenWorkspace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		bound    string // "" = master-token caller, no binding
		bodyWS   string
		wantOK   bool
		wantCode int // checked only when !wantOK
	}{
		{"bound_token_matching_body", "ws_a", "ws_a", true, 0},
		{"bound_token_foreign_body_403", "ws_a", "ws_victim", false, http.StatusForbidden},
		{"master_caller_unrestricted", "", "ws_any", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tc.bound != "" {
				req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, tc.bound))
			}
			rr := httptest.NewRecorder()
			ok := assertInternalTokenWorkspace(rr, req, tc.bodyWS)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK && rr.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", rr.Code, tc.wantCode)
			}
		})
	}
}
