package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// CLI acceptance for `crewship runtimes list` → GET
// /api/v1/runtimes/catalog (API↔CLI parity with ProvisioningHandler.
// RuntimeCatalogList). Drives the command's RunE against a mock server
// rather than hand-rolling the HTTP request.

func TestRuntimesCmdStructure(t *testing.T) {
	t.Parallel()
	if runtimesCmd.Use != "runtimes" {
		t.Errorf("runtimes Use = %q, want runtimes", runtimesCmd.Use)
	}
	have := map[string]bool{}
	for _, sub := range runtimesCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["list"] {
		t.Errorf("runtimes missing 'list' subcommand; have %v", have)
	}
}

type runtimesMock struct {
	t      *testing.T
	mu     sync.Mutex
	called bool
	rawURL string
}

func (m *runtimesMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runtimes/catalog" {
			m.t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.called = true
		m.rawURL = r.URL.String()
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"runtimes":[
			{"name":"Node.js","tool":"node","category":"runtime","versions":["22"],"default_version":"22"}
		]}`))
	})
}

func TestRuntimesListRunE_HappyPath(t *testing.T) {
	saveCLIState(t)

	m := &runtimesMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}

	if err := runtimesListCmd.RunE(runtimesListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.called {
		t.Fatal("runtimes/catalog endpoint not called")
	}
}

func TestRuntimesListRunE_ForwardsSearch(t *testing.T) {
	saveCLIState(t)

	m := &runtimesMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}

	_ = runtimesListCmd.Flags().Set("search", "node")
	defer func() { _ = runtimesListCmd.Flags().Set("search", "") }()

	if err := runtimesListCmd.RunE(runtimesListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.Contains(m.rawURL, "search=node") {
		t.Errorf("search not forwarded; raw URL = %q", m.rawURL)
	}
}

func TestRuntimesListRunE_ServerError(t *testing.T) {
	saveCLIState(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Token: "fake-token", Server: srv.URL}

	if err := runtimesListCmd.RunE(runtimesListCmd, nil); err == nil {
		t.Fatal("expected server error to bubble up")
	}
}
