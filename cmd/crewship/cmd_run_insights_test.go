package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestRunInsightsCmdStructure(t *testing.T) {
	t.Parallel()
	if runInsightsCmd.Use != "insights" {
		t.Errorf("insights Use: got %q want insights", runInsightsCmd.Use)
	}
	if runInsightsCmd.Flags().Lookup("window") == nil {
		t.Fatal("insights missing --window flag")
	}
	if dv := runInsightsCmd.Flags().Lookup("window").DefValue; dv != "24h" {
		t.Errorf("--window default: got %q want 24h", dv)
	}
}

func TestRunInsightsRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := runInsightsCmd.RunE(runInsightsCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in'; got %v", err)
	}
}

func TestRunInsightsRunE_BadWindow(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: "http://unused"}
	t.Cleanup(func() { _ = runInsightsCmd.Flags().Set("window", "24h") })
	if err := runInsightsCmd.Flags().Set("window", "1y"); err != nil {
		t.Fatalf("set --window: %v", err)
	}
	err := runInsightsCmd.RunE(runInsightsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--window") {
		t.Errorf("expected window validation error; got %v", err)
	}
}

// insightsMock records the requested URI and serves a canned insights body.
type insightsMock struct {
	t   *testing.T
	mu  sync.Mutex
	uri string
}

func (m *insightsMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/runs/insights") {
			m.t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		m.mu.Lock()
		m.uri = r.URL.RequestURI()
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"window":     "7d",
			"totals":     map[string]int{"total": 42, "succeeded": 40, "failed": 2, "running": 1},
			"duration":   map[string]int64{"p50_ms": 18400, "p95_ms": 72000},
			"by_trigger": []map[string]any{{"key": "CRON", "total": 20, "failed": 1}},
			"by_model":   []map[string]any{{"key": "claude-opus", "total": 30, "failed": 1}},
			"by_crew":    []map[string]any{{"name": "Support Crew", "total": 25, "failed": 1}},
			"top_agents": []map[string]any{{"name": "Triage Agent", "crew_name": "Support Crew", "total": 12, "failed": 0}},
			"truncated":  false,
		})
	})
}

func TestRunInsightsRunE_RendersAndPropagatesWindow(t *testing.T) {
	saveCLIState(t)
	// Keep the test hermetic: a developer shell may export CREWSHIP_SERVER
	// (per-clone dev routing), which would otherwise override the config
	// Server and trip the token host-binding guard.
	t.Setenv("CREWSHIP_SERVER", "")
	m := &insightsMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}
	t.Cleanup(func() { _ = runInsightsCmd.Flags().Set("window", "24h") })
	if err := runInsightsCmd.Flags().Set("window", "7d"); err != nil {
		t.Fatalf("set --window: %v", err)
	}

	if err := runInsightsCmd.RunE(runInsightsCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(m.uri, "window=7d") {
		t.Errorf("window not propagated: %q", m.uri)
	}
}
