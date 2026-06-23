package server

// Coverage tests for server.go: the WSHub / JournalWriter accessors,
// RegisterKeeperRoutines' nil-guard and pass-through, and a wide-options
// New() construction that exercises the optional wiring branches
// (embedded SPA + combinedHandler, host-address provider, LLM proxy,
// keeper gatekeeper, injected episodic embedder, pprof endpoint,
// public URL, workspace memory tier).

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/scheduler"
)

func TestServer_WSHubAndJournalWriterAccessors(t *testing.T) {
	s := newTestServerForT(t)
	if s.WSHub() == nil {
		t.Error("WSHub() = nil, want the constructed hub")
	}
	if s.WSHub() != s.wsHub {
		t.Error("WSHub() returns a different hub than the server's own")
	}
	if s.JournalWriter() == nil {
		t.Error("JournalWriter() = nil, want non-nil for a DB-backed server")
	}
	if s.JournalWriter() != s.journalWriter {
		t.Error("JournalWriter() returns a different writer than the server's own")
	}
}

func TestRegisterKeeperRoutines_GuardsAndPassThrough(t *testing.T) {
	logger := logging.New("error", "json", nil)

	// nil scheduler → guarded no-op.
	s := newTestServerForT(t)
	s.RegisterKeeperRoutines(nil)

	// nil DB → guarded no-op even with a scheduler.
	bare := &Server{logger: logger}
	sched := scheduler.New(s.db, nil, nil, nil, nil, nil, scheduler.Config{}, logger)
	t.Cleanup(sched.Stop)
	bare.RegisterKeeperRoutines(sched)

	// Full path: scheduler + DB. Evaluator nil-ness depends on the env
	// (aux-slot API keys), and registerKeeperPhase2Routines handles both
	// shapes — the contract here is "never panics, logs the summary".
	s.RegisterKeeperRoutines(sched)
}

// covHostAddrContainer reports an IPv6 host address so New's
// bracket-wrapping branch executes.
type covHostAddrContainer struct {
	mockContainer
}

func (c *covHostAddrContainer) HostAddress() string { return "fd00::1" }

// covEmbedder satisfies episodic.Embedder without any network.
type covEmbedder struct{}

func (covEmbedder) Embed(_ context.Context, _ string) ([]float32, error) { return []float32{1}, nil }
func (covEmbedder) Dim() int                                             { return 1 }
func (covEmbedder) Model() string                                        { return "cov-embed" }

func TestNew_WideOptionsWiring(t *testing.T) {
	t.Setenv("CREWSHIP_PUBLIC_URL", "http://cov.example.com:8080")
	t.Setenv("CREWSHIP_PPROF_ADDR", "127.0.0.1:0")

	cfg := silentCfg()
	cfg.Storage.BasePath = t.TempDir()
	cfg.Storage.LogPath = t.TempDir()
	cfg.Storage.MemoryRoot = t.TempDir()
	cfg.Container.SidecarEnabled = true
	cfg.Container.Network = "cov-test-net"
	cfg.Keeper.Enabled = true
	cfg.Keeper.OllamaURL = "http://127.0.0.1:1" // constructed, never dialled
	cfg.Keeper.Model = "test-model"
	cfg.Auth.InternalToken = "cov-token"
	cfg.Auth.GoogleClientID = "cov-google-id"
	cfg.Auth.GoogleSecret = "cov-google-secret"
	cfg.LLMProxy.Enabled = true
	cfg.LLMProxy.TokenSyncInterval = time.Hour
	cfg.LLMProxy.HealthCheckInterval = time.Hour

	webFS := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>cov-spa-shell</html>")},
	}

	logger := logging.New("error", "json", nil)
	deps := &Deps{
		DB:               openTestDB(t),
		Container:        &covHostAddrContainer{},
		State:            newMockState(),
		WebFS:            webFS,
		EpisodicEmbedder: covEmbedder{},
	}
	s := New(cfg, logger, deps)
	t.Cleanup(func() { _ = s.Shutdown() })

	if s.spaHandler == nil {
		t.Fatal("spaHandler = nil, want SPA handler with WebFS wired")
	}
	if s.tokenSyncer == nil || s.credMonitor == nil {
		t.Error("LLM proxy workers not constructed despite LLMProxy.Enabled + InternalToken")
	}
	if got := s.episodicMode(); got != "vector" {
		t.Errorf("episodicMode() = %q, want vector with injected embedder", got)
	}
	if s.pprofShutdown == nil {
		t.Error("pprofShutdown = nil, want pprof endpoint started on CREWSHIP_PPROF_ADDR")
	}
	if s.apiRouter == nil {
		t.Fatal("apiRouter = nil, want API mounted with DB + JWT secret")
	}

	// combinedHandler routing through the full middleware stack.
	h := s.httpServer.Handler

	// SPA fallback serves the embedded shell for app routes.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "cov-spa-shell") {
		t.Errorf("GET / = %d %q, want the SPA shell", w.Code, w.Body.String())
	}

	// Sensitive paths must hard-404, not 200 via the SPA shell.
	for _, p := range []string{"/.env", "/.git/HEAD", "/debug/pprof", "/package.json"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 404 {
			t.Errorf("GET %s = %d, want 404 (sensitive path)", p, w.Code)
		}
		if strings.Contains(w.Body.String(), "cov-spa-shell") {
			t.Errorf("GET %s leaked the SPA shell", p)
		}
	}

	// Mux-owned paths bypass the SPA handler.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "cov-spa-shell") {
		t.Errorf("GET /healthz = %d, want 200 from the mux, not the SPA", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/system/health", nil))
	if strings.Contains(w.Body.String(), "cov-spa-shell") {
		t.Error("GET /api/... was served by the SPA handler, want API mux")
	}
}
