package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// CLI acceptance for `crewship memory health` → GET /api/v1/memory/health
// (API↔CLI parity with MemoryHealthHandler.Get).

func TestMemoryHealthCmdStructure(t *testing.T) {
	t.Parallel()
	if memoryHealthCmd.Use != "health" {
		t.Errorf("memory health Use = %q, want health", memoryHealthCmd.Use)
	}
	if memoryHealthCmd.Flags().Lookup("crew") == nil {
		t.Errorf("memory health should expose a --crew flag")
	}
}

func TestMemoryHealthRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	flagWorkspace = ""
	if err := memoryHealthCmd.RunE(memoryHealthCmd, nil); err == nil {
		t.Fatal("expected auth error when not logged in")
	}
}

type memHealthMock struct {
	t      *testing.T
	mu     sync.Mutex
	called bool
}

func (m *memHealthMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/memory/health" {
			m.t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.called = true
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspace_id":"ws1","overall":82.5,
			"metrics":{"freshness":90,"coverage":80,"coherence":85,"efficiency":75,"reachability":80},
			"details":{}}`))
	})
}

func TestMemoryHealthRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &memHealthMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL, Workspace: "cabcdefghijklmnopqrs"}
	flagWorkspace = ""

	if err := memoryHealthCmd.RunE(memoryHealthCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.called {
		t.Fatal("memory/health endpoint not called")
	}
}

func TestMemoryHealthRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL, Workspace: "cabcdefghijklmnopqrs"}
	flagWorkspace = ""

	if err := memoryHealthCmd.RunE(memoryHealthCmd, nil); err == nil {
		t.Fatal("expected server error to bubble up")
	}
}
