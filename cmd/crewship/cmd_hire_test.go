package main

// Structural + RunE happy-path tests for the PR-D F5 hire / rehire
// CLI surface. RunE tests use httptest.NewServer to mock the API
// without standing up crewshipd.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestHireCmdStructure(t *testing.T) {
	t.Parallel()
	if hireCmd.Use != "hire" {
		t.Errorf("hire Use = %q, want hire", hireCmd.Use)
	}
	if !strings.Contains(strings.ToLower(hireCmd.Short), "ephemeral") {
		t.Errorf("hire Short should mention ephemeral; got %q", hireCmd.Short)
	}
	for _, flag := range []string{"crew", "template", "reason", "ttl", "model", "parent-lead", "yes"} {
		if hireCmd.Flags().Lookup(flag) == nil {
			t.Errorf("hire missing --%s flag", flag)
		}
	}
}

func TestRehireCmdStructure(t *testing.T) {
	t.Parallel()
	if rehireCmd.Use != "rehire <agent-slug-or-id>" {
		t.Errorf("rehire Use = %q, want \"rehire <agent-slug-or-id>\"", rehireCmd.Use)
	}
	for _, flag := range []string{"ttl", "reason", "yes"} {
		if rehireCmd.Flags().Lookup(flag) == nil {
			t.Errorf("rehire missing --%s flag", flag)
		}
	}
}

func TestHireRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := hireCmd.RunE(hireCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not logged in; got %v", err)
	}
}

func TestHireRunE_RequiresCrew(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake", Workspace: "cabcdefghijklmnopqrs"}
	err := hireCmd.RunE(hireCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew") {
		t.Errorf("expected --crew required; got %v", err)
	}
}

// hireMock captures the POST body so the RunE test can assert what
// the CLI sent. Returns a canned 201 success body shaped like the
// real API response.
type hireMock struct {
	mu     sync.Mutex
	path   string
	body   []byte
	status int
}

func (m *hireMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.path = r.URL.Path
		m.body, _ = io.ReadAll(r.Body)
		m.mu.Unlock()
		code := m.status
		if code == 0 {
			code = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{
			"id": "cabc123",
			"slug": "docs-writer-eph-abc123",
			"name": "Docs Writer",
			"status": "IDLE",
			"ephemeral": true,
			"expires_at": "2026-06-01T13:00:00Z",
			"hire_reason": "[2026-06-01T12:00:00Z] hire: ship section 7",
			"pending_review": false,
			"decision": "auto_log_journal"
		}`))
	})
}

func TestHireRunE_HappyPath_PostsBody(t *testing.T) {
	saveCLIState(t)

	m := &hireMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	// Force --yes so confirmAction doesn't block on a TTY prompt.
	if err := hireCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	if err := hireCmd.Flags().Set("crew", "docs"); err != nil {
		t.Fatalf("set --crew: %v", err)
	}
	if err := hireCmd.Flags().Set("template", "docs-writer"); err != nil {
		t.Fatalf("set --template: %v", err)
	}
	if err := hireCmd.Flags().Set("reason", "ship section 7"); err != nil {
		t.Fatalf("set --reason: %v", err)
	}
	if err := hireCmd.Flags().Set("ttl", "60"); err != nil {
		t.Fatalf("set --ttl: %v", err)
	}
	t.Cleanup(func() {
		_ = hireCmd.Flags().Set("yes", "false")
		_ = hireCmd.Flags().Set("crew", "")
		_ = hireCmd.Flags().Set("template", "")
		_ = hireCmd.Flags().Set("reason", "")
		_ = hireCmd.Flags().Set("ttl", "0")
	})

	if err := hireCmd.RunE(hireCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	path := m.path
	body := m.body
	m.mu.Unlock()

	if path != "/api/v1/agents/hire" {
		t.Errorf("path = %q, want /api/v1/agents/hire", path)
	}

	// Body must carry crew_slug (not crew_id — "docs" is a slug)
	// + template_slug + reason + ttl_minutes.
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v; raw=%s", err, body)
	}
	if got["crew_slug"] != "docs" {
		t.Errorf("crew_slug = %v, want docs", got["crew_slug"])
	}
	if got["template_slug"] != "docs-writer" {
		t.Errorf("template_slug = %v, want docs-writer", got["template_slug"])
	}
	if got["reason"] != "ship section 7" {
		t.Errorf("reason = %v, want ship section 7", got["reason"])
	}
	// JSON numbers decode as float64.
	if got["ttl_minutes"].(float64) != 60 {
		t.Errorf("ttl_minutes = %v, want 60", got["ttl_minutes"])
	}
}

func TestHireRunE_CUIDCrewGoesIntoCrewID(t *testing.T) {
	// When the user passes a CUID (looksLikeCUID returns true), the
	// CLI must use crew_id, not crew_slug — otherwise the server
	// looks the wrong column up and returns 404.
	saveCLIState(t)
	m := &hireMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	cliCfg = &cli.CLIConfig{
		Token: "fake", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL,
	}
	cuid := "cabcdefghijklmnopqrsxyz"
	_ = hireCmd.Flags().Set("yes", "true")
	_ = hireCmd.Flags().Set("crew", cuid)
	_ = hireCmd.Flags().Set("template", "docs-writer")
	_ = hireCmd.Flags().Set("reason", "x")
	t.Cleanup(func() {
		_ = hireCmd.Flags().Set("yes", "false")
		_ = hireCmd.Flags().Set("crew", "")
		_ = hireCmd.Flags().Set("template", "")
		_ = hireCmd.Flags().Set("reason", "")
	})
	if err := hireCmd.RunE(hireCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	body := m.body
	m.mu.Unlock()
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["crew_id"] != cuid {
		t.Errorf("crew_id = %v, want %q", got["crew_id"], cuid)
	}
	if _, has := got["crew_slug"]; has {
		t.Errorf("crew_slug present alongside crew_id: %v", got)
	}
}
