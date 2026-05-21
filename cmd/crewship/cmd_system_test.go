package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestSystemCmdStructure(t *testing.T) {
	t.Parallel()

	if systemCmd.Use != "system" {
		t.Errorf("system Use: got %q, want %q", systemCmd.Use, "system")
	}
	if !strings.Contains(strings.ToLower(systemCmd.Short), "system") {
		t.Errorf("system Short should mention system; got %q", systemCmd.Short)
	}

	have := map[string]bool{}
	for _, sub := range systemCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"info", "keeper", "stats", "onboarding", "aux-status"} {
		if !have[want] {
			t.Errorf("system missing subcommand %q; have %v", want, have)
		}
	}
}

func TestSystemAuxStatusCmdStructure(t *testing.T) {
	t.Parallel()

	if systemAuxStatusCmd.Use != "aux-status" {
		t.Errorf("aux-status Use: got %q, want %q", systemAuxStatusCmd.Use, "aux-status")
	}
	if !strings.Contains(strings.ToLower(systemAuxStatusCmd.Short), "auxiliary") {
		t.Errorf("aux-status Short should mention auxiliary; got %q", systemAuxStatusCmd.Short)
	}
	if !strings.Contains(systemAuxStatusCmd.Long, "aux-status") {
		t.Errorf("aux-status Long should reference command name; got %q", systemAuxStatusCmd.Long)
	}
}

func TestSystemAuxStatusRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

// systemAuxMock stubs GET /api/v1/system/aux-status with a deterministic
// payload covering all three source modes (explicit / fallback /
// unconfigured) so the CLI's table rendering and JSON pass-through can
// be exercised without standing up the full server.
type systemAuxMock struct {
	t       *testing.T
	mu      sync.Mutex
	called  bool
	path    string
	resBody string
}

func (m *systemAuxMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/aux-status" {
			m.t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.called = true
		m.path = r.URL.Path
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		body := m.resBody
		if body == "" {
			body = `{"slots":[
				{"slot":"curator","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":30000,"source":"explicit"},
				{"slot":"keeper","provider":"ollama","model":"phi3:mini","timeout_ms":3000,"source":"explicit"},
				{"slot":"behavior","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":8000,"source":"fallback"},
				{"slot":"memory_health","provider":"anthropic","model":"claude-haiku-4-5","timeout_ms":15000,"source":"fallback"},
				{"slot":"negative","provider":"","model":"","timeout_ms":0,"source":"unconfigured"}
			]}`
		}
		_, _ = w.Write([]byte(body))
	})
}

func TestSystemAuxStatusRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &systemAuxMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:  "fake-token",
		Server: srv.URL,
	}

	if err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.called {
		t.Fatal("aux-status endpoint not called")
	}
	if m.path != "/api/v1/system/aux-status" {
		t.Errorf("path = %q, want /api/v1/system/aux-status", m.path)
	}
}

func TestSystemAuxStatusRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:  "fake-token",
		Server: srv.URL,
	}

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil {
		t.Fatal("expected server error to bubble up")
	}
}

func TestDashIfEmpty(t *testing.T) {
	t.Parallel()
	if got := dashIfEmpty(""); got != "—" {
		t.Errorf("dashIfEmpty(\"\") = %q, want em-dash", got)
	}
	if got := dashIfEmpty("anthropic"); got != "anthropic" {
		t.Errorf("dashIfEmpty(%q) = %q, want passthrough", "anthropic", got)
	}
}
