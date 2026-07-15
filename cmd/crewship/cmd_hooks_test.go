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

// Structure-only tests — the RunE handlers require auth + a live server,
// which is exercised by the handler tests in internal/api/hooks_handler_test.go.
// Here we just check the cobra tree is shaped right.

func TestHooksCmdStructure(t *testing.T) {
	t.Parallel()

	if hooksCmd.Use != "hooks" {
		t.Errorf("hooks Use: got %q want %q", hooksCmd.Use, "hooks")
	}
	have := map[string]bool{}
	for _, sub := range hooksCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "enable", "disable"} {
		if !have[want] {
			t.Errorf("hooks missing subcommand %q; have %v", want, have)
		}
	}
}

func TestHooksEnableArgsValidation(t *testing.T) {
	t.Parallel()

	if err := hooksEnableCmd.Args(hooksEnableCmd, []string{}); err == nil {
		t.Error("enable with no args should error")
	}
	if err := hooksDisableCmd.Args(hooksDisableCmd, []string{}); err == nil {
		t.Error("disable with no args should error")
	}
	if err := hooksEnableCmd.Args(hooksEnableCmd, []string{"h-1"}); err != nil {
		t.Errorf("enable with one arg should pass; got %v", err)
	}
}

// hooksMock captures the hooks-list URL served and optionally answers
// /api/v1/crews so slug→id resolution (#1194) can be exercised.
type hooksMock struct {
	t     *testing.T
	mu    sync.Mutex
	hooks string // last /api/v1/hooks URL
	crews []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
}

func (m *hooksMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/hooks"):
			m.mu.Lock()
			m.hooks = r.URL.RequestURI()
			m.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"rows":[],"count":0}`))
		case r.URL.Path == "/api/v1/crews":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(m.crews)
		case strings.HasPrefix(r.URL.Path, "/api/v1/crews/"):
			w.Header().Set("Content-Type", "application/json")
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/crews/")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
		default:
			m.t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// TestHooksListRunE_SlugCrewResolvesToID is the regression test for #1194:
// `crewship hooks list --crew <slug>` must resolve the slug to the crew's
// CUID before hitting the API (the same way `resolveCrewID` already works
// for other --crew flags), rather than forwarding the raw slug as crew_id
// and 404ing.
func TestHooksListRunE_SlugCrewResolvesToID(t *testing.T) {
	saveCLIState(t)

	m := &hooksMock{
		t: t,
		crews: []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}{
			{ID: "crew-eng-cuid1", Slug: "engineering"},
			{ID: "crew-qa-cuid2", Slug: "quality"},
		},
	}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := hooksListCmd.Flags().Set("crew", "engineering"); err != nil {
		t.Fatalf("set --crew: %v", err)
	}
	t.Cleanup(func() { _ = hooksListCmd.Flags().Set("crew", "") })

	if err := hooksListCmd.RunE(hooksListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(m.hooks, "crew_id=crew-eng-cuid1") {
		t.Errorf("slug not resolved to id: %q", m.hooks)
	}
}

// TestHooksListRunE_UnknownCrewSlugError verifies an unresolvable slug
// surfaces a clear "crew not found" error instead of being forwarded
// verbatim to the hooks endpoint (which previously produced a confusing
// "API error (404): crew not found" from the wrong layer).
func TestHooksListRunE_UnknownCrewSlugError(t *testing.T) {
	saveCLIState(t)

	m := &hooksMock{t: t} // empty crews list
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = hooksListCmd.Flags().Set("crew", "does-not-exist")
	t.Cleanup(func() { _ = hooksListCmd.Flags().Set("crew", "") })

	err := hooksListCmd.RunE(hooksListCmd, nil)
	if err == nil {
		t.Fatal("expected 'crew not found' error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error should mention the slug; got %v", err)
	}
}

// TestHooksListRunE_OmitsCrewFilterWhenEmpty ensures --crew empty still
// skips resolution entirely (no /api/v1/crews call) and lists all hooks.
func TestHooksListRunE_OmitsCrewFilterWhenEmpty(t *testing.T) {
	saveCLIState(t)

	m := &hooksMock{t: t}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	_ = hooksListCmd.Flags().Set("crew", "")

	if err := hooksListCmd.RunE(hooksListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(m.hooks, "crew_id=") {
		t.Errorf("crew_id should be absent when --crew empty: %q", m.hooks)
	}
}
