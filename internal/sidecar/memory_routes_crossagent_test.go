package sidecar

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
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

const (
	crossAgentSecret = "alpha private secret\n"
	crewShared       = "crew shared note\n"
)

func seedBootTier(t *testing.T, base string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(base, "AGENT.md"), []byte(crossAgentSecret), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withCrewTier wires the CREW tier onto the fixture, which otherwise only has
// the agent tier. Without it a "scope=crew still works" test would pass for the
// wrong reason — the handler would fail on a missing engine rather than being
// allowed through the guard.
func withCrewTier(t *testing.T, s *Server) string {
	t.Helper()
	crewBase := t.TempDir()
	eng, err := memory.New(crewBase, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("memory.New(crew): %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	s.crewMemoryEngine = eng
	s.crewMemoryBase = crewBase
	if err := os.WriteFile(filepath.Join(crewBase, "CREW.md"), []byte(crewShared), 0o600); err != nil {
		t.Fatal(err)
	}
	return crewBase
}

// The crew tier is ONE directory shared by the whole crew — the orchestrator
// hands every agent with a CrewID the same CrewMemoryPath, leads included and
// not exclusively. So a sibling reading or writing scope=crew is not reaching
// another agent's memory; it is reaching the memory it is supposed to share.
//
// The first cut of this guard refused the whole legacy prefix regardless of
// scope, which protected nothing here and broke sibling access to crew-shared
// memory. These tests exist so that regression cannot come back disguised as
// tightening.
func TestLegacyMemoryRoutes_SiblingCrewScopeStillWorks(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	crewBase := withCrewTier(t, s)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	t.Run("read", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=CREW.md&scope=crew", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 200 {
			t.Fatalf("sibling crew-scope read = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "crew shared note") {
			t.Errorf("sibling did not get the crew-shared content: %s", w.Body.String())
		}
	})

	t.Run("write reaches the crew tier", func(t *testing.T) {
		body := strings.NewReader(`{"file":"CREW.md","scope":"crew","content":"written by beta\n"}`)
		r := loopbackRequest("POST", "/memory/write", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 201 && w.Code != 200 {
			t.Fatalf("sibling crew-scope write = %d, want 2xx: %s", w.Code, w.Body.String())
		}
		// The scope probe buffers and restores the body; if it consumed it the
		// handler would decode an empty body and never write these bytes.
		got, err := os.ReadFile(filepath.Join(crewBase, "CREW.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "written by beta\n" {
			t.Errorf("crew tier not written: got %q", got)
		}
	})

	t.Run("search", func(t *testing.T) {
		body := strings.NewReader(`{"query":"crew","scope":"crew"}`)
		r := loopbackRequest("POST", "/memory/search", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code == 403 {
			t.Errorf("sibling refused on crew-scope search: %s", w.Body.String())
		}
	})

	// scope=both reads the AGENT tier too, so it must still be refused — the
	// exemption is for the shared tier, not for any request that mentions it.
	t.Run("scope=both is still refused", func(t *testing.T) {
		body := strings.NewReader(`{"query":"secret","scope":"both"}`)
		r := loopbackRequest("POST", "/memory/search", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 403 {
			t.Errorf("scope=both = %d, want 403: %s", w.Code, w.Body.String())
		}
	})
}

// An empty boot slug must not disable the cross-agent check. The first cut read
// `s.memoryAgentSlug != "" && ...`, so a sidecar with no boot slug served every
// sibling the boot tier. Not reachable in production — but the guard's answer to
// "I cannot tell who the boot agent is" must be refuse, not serve.
func TestLegacyMemoryRoutes_EmptyBootSlugFailsClosed(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	s.memoryAgentSlug = ""
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
	r.Header.Set("Authorization", "Bearer tok-beta")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != 403 {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), "alpha private secret") {
		t.Errorf("empty boot slug disabled the guard and leaked the tier: %s", w.Body.String())
	}
}

// A roster entry whose token matches but whose slug is empty resolved to
// memoryAgentContextFor(""), which means "the sidecar's own agent" — silently
// promoting a slugless member to the boot agent on the MCP path.
func TestMemoryMCP_EmptySlugIdentityRefused(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	s.crewMembers = append(s.crewMembers, CrewMember{ID: "agent-3", Slug: "", AuthToken: "tok-ghost"})
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"memory.read","arguments":{"tier":"AGENT"}}}`)
	r := loopbackRequest("POST", "/mcp/memory/beta", body)
	r.Header.Set("Authorization", "Bearer tok-ghost")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if strings.Contains(w.Body.String(), "alpha private secret") {
		t.Errorf("slugless identity was promoted to the boot agent: %s", w.Body.String())
	}
	if w.Code != 403 {
		t.Errorf("status = %d, want 403: %s", w.Code, w.Body.String())
	}
}

// The refusal must not send status/reindex callers to /mcp/memory/<slug>: the
// MCP transport exposes read/write/search/append_daily and nothing else, so
// that pointer would be a dead end. A refusal that lies about the alternative
// is how a guard gets deleted by the next person who hits it.
func TestLegacyMemoryRoutes_RefusalDoesNotPromiseMissingTools(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	for _, path := range []string{"/memory/status", "/memory/reindex"} {
		method := "GET"
		if path == "/memory/reindex" {
			method = "POST"
		}
		r := loopbackRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 403 {
			t.Errorf("%s: status = %d, want 403", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "/mcp/memory/") {
			t.Errorf("%s: refusal points at an MCP tool that does not exist: %s", path, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "scope=crew") {
			t.Errorf("%s: refusal does not mention the scope that still works: %s", path, w.Body.String())
		}
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
