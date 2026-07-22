package sidecar

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// The CRE-153 chokepoint (#1295, #1303) refused a memory request that omitted
// its Authorization header or carried a forged one, and — until #1301 — also
// refused ANY authenticated sibling's scope=agent request outright rather than
// serving it. #1301 replaced that last refusal with the real fix: each sibling
// now gets its OWN agent tier (a per-slug memory.Engine, lazily built and
// cached — see peerMemoryEngineFor in memory_mcp.go) instead of either
// reaching alpha's tier or being turned away with a 403.
//
// Tests below drive s.buildHandler — the same handler newServer hands to
// http.Server — rather than calling the handler funcs directly, and assert on
// leaked/isolated CONTENT, not just status codes: a handler that resolves the
// wrong tier while still returning 200 would pass a status-only assertion.

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

// betaTierDir returns the sibling directory peerCrewMember derives for
// "beta" relative to alpha's (the boot agent's) base path.
func betaTierDir(base string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(base)), "beta", ".memory")
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

	// scope=both now serves beta its OWN agent tier plus the shared crew tier
	// — never alpha's. Before #1301 this was refused outright; the exemption
	// was for the shared tier only, not a promise that "both" would ever
	// resolve to someone else's private memory.
	t.Run("scope=both returns beta's own tier plus crew, not alpha's", func(t *testing.T) {
		betaDir := betaTierDir(base)
		if err := os.MkdirAll(betaDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(betaDir, "AGENT.md"), []byte("beta secret note\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		body := strings.NewReader(`{"query":"secret","scope":"both"}`)
		r := loopbackRequest("POST", "/memory/search", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 200 {
			t.Fatalf("scope=both = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "beta secret note") {
			t.Errorf("scope=both did not include beta's own agent-tier hit: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("scope=both leaked alpha's private tier to beta: %s", w.Body.String())
		}
	})
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

// A token matching no crew member must not reach a memory handler. Before this
// fix `Authorization: Bearer anything` was not a token-less downgrade, so the
// chokepoint let it through, and no legacy handler resolved identity behind it
// — a cheaper bypass than the one #1295 closed. Independent of #1301 (that
// change is about a VALID sibling token; this is about a token that matches no
// one at all), so still refused outright.
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

// TestLegacyMemoryRoutes_SiblingTokenGetsOwnTier is the #1301 acceptance test:
// a sibling holding its OWN valid token now reaches its OWN agent tier through
// the legacy routes, never alpha's — inverted from the pre-#1301 behaviour
// (TestLegacyMemoryRoutes_SiblingTokenRefused, which pinned an outright 403).
// Isolation is the property that matters: alpha's tier is untouched on disk,
// beta's write lands in beta's own directory, and beta's read sees its own
// content, never alpha's.
func TestLegacyMemoryRoutes_SiblingTokenGetsOwnTier(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base)

	t.Run("read starts empty, not alpha's tier", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 404 {
			t.Fatalf("status = %d, want 404 (beta has no AGENT.md yet): %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("beta's read leaked alpha's tier: %s", w.Body.String())
		}
	})

	t.Run("write reaches beta's OWN tier, not alpha's", func(t *testing.T) {
		body := strings.NewReader(`{"file":"AGENT.md","content":"beta's own note\n"}`)
		r := loopbackRequest("POST", "/memory/write", body)
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 201 {
			t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
		}

		got, err := os.ReadFile(filepath.Join(base, "AGENT.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != crossAgentSecret {
			t.Errorf("beta's write clobbered alpha's AGENT.md: got %q, want %q", got, crossAgentSecret)
		}

		gotBeta, err := os.ReadFile(filepath.Join(betaTierDir(base), "AGENT.md"))
		if err != nil {
			t.Fatalf("beta's own AGENT.md missing: %v", err)
		}
		if string(gotBeta) != "beta's own note\n" {
			t.Errorf("beta's own tier = %q, want %q", gotBeta, "beta's own note\n")
		}
	})

	t.Run("subsequent read now sees beta's own write, not alpha's", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "beta's own note") {
			t.Errorf("beta did not read back its own write: %s", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("beta's read leaked alpha's tier: %s", w.Body.String())
		}
	})

	// The alpha (boot) tier must still be reachable, by the boot agent, exactly
	// as before — #1301 changes routing for SIBLINGS, not for the boot agent.
	t.Run("alpha's own read is unaffected", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/read?file=AGENT.md", nil)
		r.Header.Set("Authorization", "Bearer tok-alpha")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "alpha private secret") {
			t.Errorf("alpha lost access to its own tier: %s", w.Body.String())
		}
	})
}

// TestLegacyMemoryRoutes_SiblingSearchStatusReindexOwnTier covers the harder
// half of #1301: search/status/reindex run through a real memory.Engine (FTS5
// over SQLite), not a plain file read, so per-agent means per-agent ENGINE
// instances — peerMemoryEngineFor's lazy construction + cache, not just a
// base-path swap.
func TestLegacyMemoryRoutes_SiblingSearchStatusReindexOwnTier(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	h := s.buildHandler(nil)
	seedBootTier(t, base) // alpha: "alpha private secret\n"
	// newLegacyMemoryRouteServer constructs alpha's engine before this file
	// exists, unlike peerMemoryEngineFor (which reindexes on construction for
	// a lazily-built peer engine) — reindex explicitly so alpha's own search
	// below isn't comparing against a stale, pre-seed index.
	if err := s.memoryEngine.Reindex(); err != nil {
		t.Fatalf("reindex alpha's engine: %v", err)
	}

	betaDir := betaTierDir(base)
	if err := os.MkdirAll(betaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(betaDir, "AGENT.md"), []byte("beta needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("search finds beta's own content, not alpha's", func(t *testing.T) {
		r := loopbackRequest("POST", "/memory/search", strings.NewReader(`{"query":"needle"}`))
		r.Header.Set("Authorization", "Bearer tok-beta")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "beta needle") {
			t.Errorf("beta's search did not find its own content: %s", w.Body.String())
		}

		r2 := loopbackRequest("POST", "/memory/search", strings.NewReader(`{"query":"secret"}`))
		r2.Header.Set("Authorization", "Bearer tok-alpha")
		r2.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != 200 || !strings.Contains(w2.Body.String(), "alpha private secret") {
			t.Errorf("alpha's own search broke: %d %s", w2.Code, w2.Body.String())
		}
		if strings.Contains(w2.Body.String(), "beta needle") {
			t.Errorf("alpha's search leaked beta's content: %s", w2.Body.String())
		}
	})

	t.Run("status succeeds for beta's own engine", func(t *testing.T) {
		r := loopbackRequest("GET", "/memory/status", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
	})

	t.Run("reindex reindexes beta's own engine, not alpha's", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(betaDir, "pins.md"), []byte("beta pinned fact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := loopbackRequest("POST", "/memory/reindex", nil)
		r.Header.Set("Authorization", "Bearer tok-beta")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}

		r2 := loopbackRequest("POST", "/memory/search", strings.NewReader(`{"query":"pinned"}`))
		r2.Header.Set("Authorization", "Bearer tok-beta")
		r2.Header.Set("Content-Type", "application/json")
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != 200 || !strings.Contains(w2.Body.String(), "beta pinned fact") {
			t.Errorf("reindex did not pick up beta's new file: %d %s", w2.Code, w2.Body.String())
		}
	})

	t.Run("engine is cached across requests, not reconstructed each time", func(t *testing.T) {
		eng1, dir1, err := s.peerMemoryEngineFor(context.Background(), "beta")
		if err != nil {
			t.Fatal(err)
		}
		eng2, dir2, err := s.peerMemoryEngineFor(context.Background(), "beta")
		if err != nil {
			t.Fatal(err)
		}
		if eng1 != eng2 {
			t.Error("peerMemoryEngineFor did not reuse the cached engine for the same slug")
		}
		if dir1 != dir2 || dir1 != betaDir {
			t.Errorf("dir1=%q dir2=%q, want both = %q", dir1, dir2, betaDir)
		}
	})
}

// Concurrent first-access requests for the SAME sibling slug must converge on
// ONE engine instance, not race to construct several (each holding its own
// SQLite connection to the same index.sqlite — wasteful at best, and a
// use-after-close hazard if one loser's engine gets closed while the winner's
// is still in use).
func TestPeerMemoryEngineFor_ConcurrentFirstAccessConverges(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	seedBootTier(t, base)

	const n = 16
	engines := make([]*memory.Engine, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			eng, _, err := s.peerMemoryEngineFor(context.Background(), "beta")
			if err != nil {
				t.Errorf("peerMemoryEngineFor: %v", err)
				return
			}
			engines[i] = eng
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if engines[i] != engines[0] {
			t.Fatalf("goroutine %d got a different engine instance than goroutine 0 — the cache did not converge", i)
		}
	}
}

// closePeerMemoryEngines must close every cached engine at shutdown (mirrors
// memoryEngine/crewMemoryEngine) and tolerate an empty/nil cache — most tests
// in this package build a Server by hand and never populate one.
func TestClosePeerMemoryEngines(t *testing.T) {
	s, base := newLegacyMemoryRouteServer(t, true)
	seedBootTier(t, base)

	// Nil/empty cache: must not panic.
	s.closePeerMemoryEngines()

	eng, dir, err := s.peerMemoryEngineFor(context.Background(), "beta")
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected a non-empty base path for beta's tier")
	}

	s.closePeerMemoryEngines()

	if _, err := eng.Status(context.Background()); err == nil {
		t.Error("expected a closed peer engine to error on further use")
	}
}

// The boot agent itself must keep working through the legacy routes — those
// routes serve its tier and it is the one agent for which that was always
// correct.
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
// resolves per-agent identity properly and serves beta its own tier.
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
