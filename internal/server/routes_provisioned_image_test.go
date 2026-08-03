package server

// #1717 — the two internal start routes started a crew from the bare default
// runtime image instead of the devcontainer it had been provisioned into,
// because they built a provider.CrewConfig by hand and never looked up
// cached_image. What the operator then got — on the terminal, on the dashboard's
// start button — was a crew with none of its own toolchain.
//
// The assertion is the value that decides which image docker brings up
// (CrewConfig.CachedImage), taken from the provider's own point of view. A test
// that asserted "the handler called the completer" would pass on a completer
// that returned nothing.

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// imageRecordingContainer captures the config each start is asked for.
type imageRecordingContainer struct {
	mockContainer
	mu   sync.Mutex
	cfgs []provider.CrewConfig
}

func (c *imageRecordingContainer) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	c.mu.Lock()
	c.cfgs = append(c.cfgs, cfg)
	c.mu.Unlock()
	return "container-" + cfg.ID, nil
}

func (c *imageRecordingContainer) last() (provider.CrewConfig, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cfgs) == 0 {
		return provider.CrewConfig{}, false
	}
	return c.cfgs[len(c.cfgs)-1], true
}

// seedProvisionedCrew writes a crew that has been provisioned: it carries a
// cached image tag, which is not the default runtime image.
func seedProvisionedCrew(t *testing.T, s *Server, crewID, slug, cachedImage string) {
	t.Helper()
	mustExec(t, s.db, `INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws-img','Img','ws-img')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug, cached_image)
		VALUES (?, 'ws-img', ?, ?, ?)`, crewID, slug, slug, cachedImage)
}

func TestContainerStartUsesTheCrewsProvisionedImage(t *testing.T) {
	s := newTestServerWithDeps(t)
	rec := &imageRecordingContainer{}
	s.container = rec
	seedProvisionedCrew(t, s, "crew-img-1", "engineering", "crewship-cache:db6c6fcbdb34")

	req := httptest.NewRequest("POST", "/crews/crew-img-1/container/start", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	cfg, ok := rec.last()
	if !ok {
		t.Fatal("the provider was never asked to start a crew")
	}
	if cfg.CachedImage != "crewship-cache:db6c6fcbdb34" {
		t.Errorf("CachedImage = %q, want the crew's provisioned tag — with it empty the docker "+
			"provider falls back to the global default runtime image and the crew comes up without "+
			"any of the tools it was provisioned with (#1717)", cfg.CachedImage)
	}
	if cfg.Slug != "engineering" {
		t.Errorf("Slug = %q, want engineering — the container name is derived from it", cfg.Slug)
	}
}

func TestAgentStartUsesTheCrewsProvisionedImage(t *testing.T) {
	s := newTestServerWithDeps(t)
	rec := &imageRecordingContainer{}
	s.container = rec
	seedProvisionedCrew(t, s, "crew-img-2", "research", "crewship-cache:aa11bb22cc33")

	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.runCtx = runCtx
	s.orchestrator.StopAccepting()

	body := `{"workspace_id":"ws-img","crew_id":"crew-img-2","crew_slug":"research",` +
		`"agent_slug":"bob","session_id":"sess-img","timeout_seconds":1}`
	req := httptest.NewRequest("POST", "/agents/agent-img/start", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	cfg, ok := rec.last()
	if !ok {
		t.Fatal("the provider was never asked to start a crew")
	}
	if cfg.CachedImage != "crewship-cache:aa11bb22cc33" {
		t.Errorf("CachedImage = %q, want the crew's provisioned tag (#1717)", cfg.CachedImage)
	}
}
