package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type initFixtureTransport struct {
	allowed string
	base    http.RoundTripper
}

func (tr initFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// In particular, never let a regression bootstrap localhost:8080.
	if req.URL.Scheme+"://"+req.URL.Host != tr.allowed {
		return nil, fmt.Errorf("blocked non-fixture init request to %s", req.URL)
	}
	return tr.base.RoundTrip(req)
}

func TestInitServerSelection(t *testing.T) {
	guardCLIState(t)
	if initCmd.LocalNonPersistentFlags().Lookup("server") != nil {
		t.Error("init must inherit the root server flag instead of shadowing it")
	}
	for _, tc := range []struct {
		name       string
		profile    string
		env        bool
		flag       string
		missing    bool
		serverless bool
	}{
		{name: "config"},
		{name: "env_over_config", env: true},
		{name: "current_profile_over_env", profile: "current", env: true},
		{name: "env_profile_over_env_server", profile: "env", env: true},
		{name: "flag_profile_over_env_server", profile: "flag", env: true},
		{name: "explicit_server_over_profile_and_env", profile: "flag", env: true, flag: "--server"},
		{name: "short_server_over_profile_and_env", profile: "flag", env: true, flag: "-s"},
		{name: "unknown_profile_fails_closed", profile: "flag", env: true, missing: true},
		{name: "serverless_profile_fails_closed", profile: "flag", env: true, serverless: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetCLIState()
			t.Cleanup(resetCLIState)
			t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "cli-config.yaml"))
			t.Setenv("CREWSHIP_SERVER", "")
			t.Setenv("CREWSHIP_PROFILE", "")
			t.Setenv("CREWSHIP_TOKEN", "synthetic-env-token")
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/bootstrap" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Workspace-ID") != "" {
					t.Error("bootstrap must remain unauthenticated and unscoped")
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode bootstrap: %v", err)
				}
				if body["email"] != "admin@example.invalid" || body["full_name"] != "Fixture Admin" || body["password"] != "fixture-password" {
					t.Error("bootstrap payload differs from synthetic input")
				}
				w.Header().Set("Content-Type", "application/json")
				if call > 1 {
					w.WriteHeader(http.StatusGone)
					fmt.Fprint(w, `{"error":"Already initialized — please log in at /login instead"}`)
					return
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"user_id":"fixture-user","email":"admin@example.invalid","workspace_id":"fixture-workspace","cli_token":"synthetic-bootstrap-token"}`)
			}))
			defer s.Close()
			oldTransport := http.DefaultTransport
			http.DefaultTransport = initFixtureTransport{allowed: s.URL, base: oldTransport}
			t.Cleanup(func() { http.DefaultTransport = oldTransport })

			// Losing candidates are deliberately unreachable, and the transport
			// denies them before dialing. Only the expected winner is a fixture.
			cfg := &cli.CLIConfig{Server: "http://config.invalid", Token: "synthetic-stored-token", Workspace: "synthetic-workspace"}
			if tc.env {
				t.Setenv("CREWSHIP_SERVER", "http://env.invalid")
			}
			if tc.profile != "" {
				cfg.Servers = map[string]*cli.ServerProfile{"fixture": {Server: "http://profile.invalid", Token: "synthetic-profile-token"}}
				switch tc.profile {
				case "current":
					cfg.Current = "fixture"
				case "env":
					t.Setenv("CREWSHIP_PROFILE", "fixture")
				}
			}
			args := []string{"init", "--email", "admin@example.invalid", "--name", "Fixture Admin", "--password-stdin"}
			switch {
			case tc.flag != "":
				args = append(args, tc.flag, s.URL)
			case tc.profile != "":
				cfg.Servers["fixture"].Server = s.URL
			case tc.env:
				t.Setenv("CREWSHIP_SERVER", s.URL)
			default:
				cfg.Server = s.URL
			}
			if tc.profile == "flag" {
				args = append(args, "--profile", "fixture")
			}
			if tc.missing {
				delete(cfg.Servers, "fixture")
			}
			if tc.serverless {
				cfg.Servers["fixture"].Server = ""
			}
			if err := cli.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			// A local command tree exercises parsing and the real config overlay
			// without executing or sorting the shared root command.
			root := &cobra.Command{Use: "crewship", PersistentPreRun: rootCmd.PersistentPreRun, SilenceErrors: true, SilenceUsage: true}
			root.PersistentFlags().AddFlagSet(rootCmd.PersistentFlags())
			init := &cobra.Command{Use: "init", RunE: initCmd.RunE}
			init.Flags().AddFlagSet(initCmd.LocalNonPersistentFlags())
			root.AddCommand(init)
			// Also exercise the actual registered root/child boundary once,
			// under the same fixture-only transport guard.
			if tc.name == "env_over_config" {
				root = rootCmd
				t.Cleanup(func() {
					rootCmd.SetArgs(nil)
					rootCmd.SetIn(nil)
				})
			}
			run := func() error {
				root.SetArgs(args)
				root.SetIn(strings.NewReader("fixture-password\n"))
				return root.Execute()
			}
			output, err := captureStderrCov(t, run)
			if tc.missing || tc.serverless {
				if err == nil || !strings.Contains(err.Error(), `profile "fixture" has no server URL`) || calls.Load() != 0 {
					t.Fatalf("selected profile must report its missing server URL without selecting a fallback: err=%v calls=%d", err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatalf("first init should reach selected fixture: %v", err)
			}
			if !strings.Contains(output, "crewship login --server "+s.URL+" --token synthetic-bootstrap-token") {
				t.Errorf("login hint does not identify selected server: %s", output)
			}
			_, err = captureStderrCov(t, run)
			if err == nil || !strings.Contains(err.Error(), "Already initialized") {
				t.Errorf("repeat init should surface selected server refusal: %v", err)
			}
			if calls.Load() != 2 {
				t.Errorf("selected server calls = %d, want 2", calls.Load())
			}
		})
	}
}
