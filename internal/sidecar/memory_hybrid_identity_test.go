package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1348: the hybrid branch of POST /memory/search forwarded to the host with
// only the sidecar's IPC token — the ACTING agent identity the memory
// chokepoint had just validated was dropped, so host-side scope="own"
// resolved to the internal-token identity for every sibling in the crew
// container. The forward must now carry the acting agent's slug, derived
// EXCLUSIVELY from the token-resolved identity (never from the URL or the
// request payload), and requests whose identity cannot be resolved must never
// be forwarded at all.
//
// Tests drive s.buildHandler — the production router including the CRE-153
// memory chokepoint — with a stub host recording what the sidecar forwarded.

// hybridForwardStub is an httptest host that records whether the forward
// happened, on which path, and with which acting-agent header.
type hybridForwardStub struct {
	srv       *httptest.Server
	forwarded bool
	path      string
	slug      string
	token     string
}

func newHybridForwardStub(t *testing.T) *hybridForwardStub {
	t.Helper()
	st := &hybridForwardStub{}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st.forwarded = true
		st.path = r.URL.Path
		st.slug = r.Header.Get("X-Acting-Agent-Slug")
		st.token = r.Header.Get("X-Internal-Token")
		writeJSONResponse(w, http.StatusOK, map[string]any{"query": "x", "count": 0, "hits": []any{}})
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func hybridSearchViaRouter(t *testing.T, s *Server, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	h := s.buildHandler(nil)
	body := strings.NewReader(`{"query":"secret","hybrid":true,"scope":"agent"}`)
	r := loopbackRequest("POST", "/memory/search", body)
	r.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A sibling's valid token must forward the SIBLING's slug — not the boot
// agent's — and the boot agent's own token must forward the boot slug. The
// header value comes from the token roster, so a sibling cannot end up with
// the internal-token (boot) identity host-side.
func TestHybridForward_CarriesTokenResolvedActingSlug(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bearer string
		want   string
	}{
		{"sibling token forwards sibling slug", "tok-beta", "beta"},
		{"boot token forwards boot slug", "tok-alpha", "alpha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newLegacyMemoryRouteServer(t, true)
			stub := newHybridForwardStub(t)
			s.ipc.BaseURL = stub.srv.URL
			s.ipc.Token = "crew-ipc-token"

			w := hybridSearchViaRouter(t, s, tc.bearer)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}
			if !stub.forwarded {
				t.Fatal("request was not forwarded to the host")
			}
			if stub.path != "/api/v1/internal/memory/search/hybrid" {
				t.Errorf("forward path = %q, want /api/v1/internal/memory/search/hybrid", stub.path)
			}
			if stub.slug != tc.want {
				t.Errorf("X-Acting-Agent-Slug = %q, want %q", stub.slug, tc.want)
			}
			if stub.token != "crew-ipc-token" {
				t.Errorf("X-Internal-Token = %q, want crew-ipc-token", stub.token)
			}
		})
	}
}

// On a crew WITH per-agent tokens, a token-less hybrid request is a downgrade
// attempt and must be refused by the memory chokepoint (#1341) BEFORE any IPC
// forward happens — the host must never even see the request.
func TestHybridForward_TokenlessDowngradeNeverForwards(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)
	stub := newHybridForwardStub(t)
	s.ipc.BaseURL = stub.srv.URL
	s.ipc.Token = "crew-ipc-token"

	w := hybridSearchViaRouter(t, s, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if stub.forwarded {
		t.Fatal("token-less downgrade was forwarded to the host — the chokepoint must refuse it first")
	}
}

// A forged token (matches no crew member) must likewise be refused with no
// forward.
func TestHybridForward_ForgedTokenNeverForwards(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)
	stub := newHybridForwardStub(t)
	s.ipc.BaseURL = stub.srv.URL
	s.ipc.Token = "crew-ipc-token"

	w := hybridSearchViaRouter(t, s, "totally-forged")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if stub.forwarded {
		t.Fatal("forged-token request was forwarded to the host")
	}
}

// A genuinely token-less (legacy, un-upgraded) deployment has exactly one
// possible acting identity — the boot agent the sidecar was minted for — so
// the forward carries the boot slug from the sidecar's own IPC config. That
// value comes from the orchestrator-provisioned config, never from the
// caller, so it is not spoofable from inside the container.
func TestHybridForward_LegacyTokenlessForwardsBootSlug(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, false)
	stub := newHybridForwardStub(t)
	s.ipc.BaseURL = stub.srv.URL
	s.ipc.Token = "crew-ipc-token"

	w := hybridSearchViaRouter(t, s, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !stub.forwarded {
		t.Fatal("legacy token-less request was not forwarded")
	}
	if stub.slug != "alpha" {
		t.Errorf("X-Acting-Agent-Slug = %q, want boot slug alpha", stub.slug)
	}
}

// When no acting identity can be resolved AT ALL (legacy deployment whose IPC
// config carries no boot slug), the sidecar fails closed instead of
// forwarding an identity-less request the host would have to guess about.
func TestHybridForward_NoResolvableIdentityRefuses(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, false)
	stub := newHybridForwardStub(t)
	s.ipc.BaseURL = stub.srv.URL
	s.ipc.Token = "crew-ipc-token"
	s.ipc.AgentSlug = ""

	w := hybridSearchViaRouter(t, s, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if stub.forwarded {
		t.Fatal("identity-less request was forwarded to the host")
	}
}

// The forwarded body must keep the scope translation + crew_id the host
// contract expects (regression guard for the path move to the internal
// route).
func TestHybridForward_BodyContractUnchanged(t *testing.T) {
	s, _ := newLegacyMemoryRouteServer(t, true)
	var gotBody map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSONResponse(w, http.StatusOK, map[string]any{"count": 0})
	}))
	t.Cleanup(stub.Close)
	s.ipc.BaseURL = stub.URL
	s.ipc.Token = "crew-ipc-token"

	h := s.buildHandler(nil)
	body := strings.NewReader(`{"query":"secret","hybrid":true,"scope":"crew","limit":7}`)
	r := loopbackRequest("POST", "/memory/search", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer tok-beta")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if gotBody["scope"] != "crew_shared" {
		t.Errorf("host scope = %v, want crew_shared", gotBody["scope"])
	}
	if gotBody["crew_id"] != "crew-1" {
		t.Errorf("host crew_id = %v, want crew-1", gotBody["crew_id"])
	}
	if gotBody["limit"] != float64(7) {
		t.Errorf("host limit = %v, want 7", gotBody["limit"])
	}
}
