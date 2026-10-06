package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestMCPServerlessExplicitConfigCannotLoseSelectedOrigin(t *testing.T) {
	for _, broken := range []struct{ name, content string }{{"empty", ""}, {"empty-map", "{}\n"}, {"token-only", "token: stored-test-token\n"}, {"blank-server", "server: '   '\n"}} {
		t.Run(broken.name, func(t *testing.T) {
			saveCLIState(t)
			flagServer, flagProfile, flagWorkspace = "", "", ""
			t.Setenv("CREWSHIP_PROFILE", "")
			selected := httptest.NewServer(http.NotFoundHandler())
			defer selected.Close()
			var calls, tokenCalls atomic.Int64
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") == "Bearer synthetic-environment-token" {
					tokenCalls.Add(1)
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer fallback.Close()
			t.Setenv("CREWSHIP_SERVER", fallback.URL)
			t.Setenv("CREWSHIP_TOKEN", "synthetic-environment-token")
			path := filepath.Join(t.TempDir(), "selected.yaml")
			// A persisted selected profile defeats the stale fallback environment.
			good := "current: selected\nservers:\n  selected:\n    server: " + selected.URL + "\n    token: stored-test-token\n"
			if err := os.WriteFile(path, []byte(good), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := cli.LoadConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cliCfg = cfg.WithActiveProfile("")
			if got := cli.EffectiveServer("", "", cliCfg); got != selected.URL {
				t.Fatalf("selected origin not established: %s", got)
			}
			previous := cliCfg
			// A truncated/replaced selected file must not silently abandon that origin.
			if err := os.WriteFile(path, []byte(broken.content), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newMCPServeCommand()
			cmd.SetArgs([]string{"--credential-config", path, "--catalog", "server"})
			err = cmd.Execute()
			if calls.Load() != 0 || tokenCalls.Load() != 0 {
				t.Errorf("unselected fallback contacted: requests=%d bearer_requests=%d", calls.Load(), tokenCalls.Load())
			}
			if err == nil || !strings.Contains(err.Error(), "selected credential configuration has no valid server") {
				t.Errorf("serverless selected file did not fail at startup: %v", err)
			}
			if cliCfg != previous {
				t.Error("failed explicit selection replaced the prior login/origin")
			}
		})
	}
}

// Catalog HTTP 404 deliberately stops startup after the target/auth boundary,
// so these controls never enter MCP's process-wide stdin transport.
func TestMCPSelectedConfigStartup(t *testing.T) {
	for _, tc := range []struct {
		name, content, profile, server, wantError string
		wantSelected                              int64
	}{
		{name: "valid-current-profile", content: "current: selected\nservers:\n  selected:\n    server: SELECTED\n    token: stored-test-token\n", wantError: "server catalog unavailable: HTTP 404", wantSelected: 1},
		{name: "explicit-server-empty-file", server: "SELECTED", wantError: "server catalog unavailable: HTTP 404", wantSelected: 1},
		{name: "explicit-server-token-only", content: "token: stored-test-token\n", server: "SELECTED", wantError: "server catalog unavailable: HTTP 404", wantSelected: 1},
		{name: "unknown-current", content: "current: missing\nserver: SELECTED\n", wantError: "selected profile is not configured"},
		{name: "unknown-explicit-profile-with-server", profile: "missing", server: "SELECTED", wantError: "selected profile is not configured"},
		{name: "unknown-environment-profile", profile: "env:missing", content: "server: SELECTED\n", wantError: "selected profile is not configured"},
		{name: "invalid-file-server", content: "server: file:///tmp/unsupported\n", wantError: "selected credential configuration has no valid server"},
		{name: "invalid-explicit-server", server: "file:///tmp/unsupported", wantError: "selected credential configuration has no valid server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			flagServer, flagProfile, flagWorkspace = "", "", ""
			t.Setenv("CREWSHIP_PROFILE", "")
			var selectedCalls, fallbackCalls atomic.Int64
			selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selectedCalls.Add(1)
				if r.URL.Path != "/openapi.json" || r.Header.Get("Authorization") != "Bearer synthetic-environment-token" {
					t.Errorf("unexpected catalog request %s with bearer %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(http.StatusNotFound)
			}))
			defer selected.Close()
			fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fallbackCalls.Add(1)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer fallback.Close()
			t.Setenv("CREWSHIP_SERVER", fallback.URL)
			t.Setenv("CREWSHIP_TOKEN", "synthetic-environment-token")
			flagServer = strings.ReplaceAll(tc.server, "SELECTED", selected.URL)
			if strings.HasPrefix(tc.profile, "env:") {
				t.Setenv("CREWSHIP_PROFILE", strings.TrimPrefix(tc.profile, "env:"))
			} else {
				flagProfile = tc.profile
			}
			previous := &cli.CLIConfig{Server: selected.URL, Token: "prior-test-token"}
			cliCfg = previous
			path := filepath.Join(t.TempDir(), "selected.yaml")
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(tc.content, "SELECTED", selected.URL)), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newMCPServeCommand()
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"--credential-config", path, "--catalog", "server"})
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("startup error = %v, want %q", err, tc.wantError)
			}
			if selectedCalls.Load() != tc.wantSelected || fallbackCalls.Load() != 0 {
				t.Errorf("selected requests=%d (want %d), fallback requests=%d", selectedCalls.Load(), tc.wantSelected, fallbackCalls.Load())
			}
			if tc.wantSelected == 0 && cliCfg != previous {
				t.Error("failed validation replaced prior CLI state")
			}
		})
	}
}
