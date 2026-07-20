package sidecar

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CRE-153 chokepoint (#1295) refused a memory request that omitted its
// Authorization header, and stopped there. Both tests below FAIL on that
// commit: the guard read a *missing* header as the only way to reach another
// agent's tier, when it was merely the cheapest.
//
// The five legacy /memory/* handlers resolve their tier from s.agentMemoryBase
// — the boot agent's memory — and call actingIdentity nowhere (verified: no
// identity resolution in handleMemory{Read,Write,Search,Status,Reindex}). So
// anything that got past "is the header present" was served alpha's private
// tier regardless of who was asking.
//
// Both tests drive s.buildHandler — the same handler newServer hands to
// http.Server — rather than calling the handler funcs directly. That
// distinction is the whole point: #1274's test called
// handleMemoryMCPForAgent(w, req, "beta") directly, proved nothing about what
// was actually registered, and let the five legacy routes ship exposed.
//
// They assert on the leaked CONTENT, not just the status code: a guard that
// returns 403 while still writing the body would pass a status-only assertion.

const crossAgentSecret = "alpha private secret\n"

func seedBootTier(t *testing.T, base string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, "AGENT.md"), []byte(crossAgentSecret), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A token matching no crew member must not reach a memory handler. Before this
// fix `Authorization: Bearer anything` was not a token-less downgrade, so the
// chokepoint let it through, and no legacy handler resolved identity behind it
// — a cheaper bypass than the one #1295 closed.
func TestLegacyMemoryRoutes_ForgedTokenRefused(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	// Near-misses on the real token, not just obvious junk: a prefix, a
	// suffix, and an unrelated string. (" tok-alpha" is deliberately NOT here —
	// bearerToken trims surrounding whitespace, so that IS alpha's token and
	// serving it is correct.)
	for _, forged := range []string{"totally-made-up", "tok-alph", "tok-alpha-", "tok-beta-x"} {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer "+forged)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 403 {
			t.Errorf("forged token %q: status = %d, want 403", forged, w.Code)
		}
		if strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("forged token %q: LEAKED the boot agent's private tier: %s", forged, w.Body.String())
		}
	}
}

// A sibling holding its OWN valid token must not read or overwrite the boot
// agent's tier through the legacy routes. This is the impact #1295's commit
// message claimed to have closed and did not: beta's token is genuine, so the
// token-less check never fired, and the handlers then served alpha's tier
// because that is the only tier they know how to serve.
func TestLegacyMemoryRoutes_SiblingTokenRefused(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	t.Run("read", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 403 {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("beta read alpha's private AGENT.md: %s", w.Body.String())
		}
	})

	t.Run("write leaves the boot tier untouched", func(t *testing.T) {
		body := strings.NewReader(`{"file":"AGENT.md","content":"clobbered by beta\n"}`)
		r := loopbackRequest("POST", "/memory/write", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 403 {
			t.Errorf("status = %d, want 403", w.Code)
		}
		// The status code is not the property that matters — the bytes on disk
		// are. A refusal that still wrote would pass the check above.
		got, err := os.ReadFile(filepath.Join(base, "AGENT.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != crossAgentSecret {
			t.Errorf("beta clobbered alpha's AGENT.md: got %q, want %q", got, crossAgentSecret)
		}
	})

	// The refusal has to tell the sibling where its own memory actually lives,
	// otherwise the fix reads as "memory is broken" and the next person removes
	// the guard.
	t.Run("refusal points at the per-agent route", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if !strings.Contains(w.Body.String(), "/mcp/memory/beta") {
			t.Errorf("refusal does not point at the per-agent route: %s", w.Body.String())
		}
	})
}

// The boot agent itself must keep working through the legacy routes — those
// routes serve its tier and it is the one agent for which that is correct. A
// guard that also locked out the boot agent would be caught here rather than in
// production.
func TestLegacyMemoryRoutes_BootAgentStillWorks(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
	r.Header.Set("Authorization", "Bearer tok-alpha")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 200 {
		t.Fatalf("boot agent status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "alpha private secret") {
		t.Errorf("boot agent did not get its own content: %s", w.Body.String())
	}
}

// A sibling reaching the MCP transport must NOT be refused — that route
// resolves per-agent identity properly and serves beta its own tier. The
// cross-agent refusal is scoped to the legacy routes; applying it to the whole
// memory prefix would break the surface siblings are supposed to use.
func TestMemoryMCPRoute_SiblingStillAllowed(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	r := loopbackRequest("POST", "/mcp/memory/beta", body)
	r.Header.Set("Authorization", "Bearer tok-beta")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code == 403 {
		t.Fatalf("sibling refused on its own per-agent MCP route: %s", w.Body.String())
	}
}
