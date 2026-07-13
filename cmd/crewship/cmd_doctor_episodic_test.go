//go:build !clionly

package main

// W2 (Release 1.0 hardening): `crewship doctor` surfaces the episodic
// recall mode reported by the server's /healthz endpoint. The helper is
// table-tested against a stub HTTP server — no crewshipd, Ollama, or
// Docker involved.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestCheckEpisodicRecallMode(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		wantStatus string
		wantDetail string // substring
		wantHint   string // substring, "" = don't care
	}{
		{
			name:       "vector mode passes",
			body:       `{"status":"ok","episodic":"vector"}`,
			status:     http.StatusOK,
			wantStatus: "PASS",
			wantDetail: "vector",
		},
		{
			name:       "sparse-only warns with enable hint",
			body:       `{"status":"ok","episodic":"sparse-only"}`,
			status:     http.StatusOK,
			wantStatus: "WARN",
			wantDetail: "sparse-only",
			wantHint:   "KEEPER_OLLAMA_URL",
		},
		{
			name:       "older server without the field is informational",
			body:       `{"status":"ok"}`,
			status:     http.StatusOK,
			wantStatus: "INFO",
			wantDetail: "does not report",
		},
		{
			name:       "non-200 healthz is informational, not a duplicate FAIL",
			body:       `oops`,
			status:     http.StatusBadGateway,
			wantStatus: "INFO",
			wantDetail: "502",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got := checkEpisodicRecallMode(context.Background(), srv.URL)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (detail: %s)", got.status, tc.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tc.wantDetail) {
				t.Fatalf("detail %q does not contain %q", got.detail, tc.wantDetail)
			}
			if tc.wantHint != "" && !strings.Contains(got.hint, tc.wantHint) {
				t.Fatalf("hint %q does not contain %q", got.hint, tc.wantHint)
			}
		})
	}
}

func TestCheckEpisodicRecallMode_ServerUnreachable(t *testing.T) {
	// Closed port: the dedicated "server reachable" check already FAILs
	// loudly when the daemon is down — this check must not double-report.
	got := checkEpisodicRecallMode(context.Background(), "http://127.0.0.1:1")
	if got.status != "INFO" {
		t.Fatalf("status = %q, want INFO when server is unreachable", got.status)
	}
}

// Regression (#1003): runCheckEpisodicRecallMode used to call
// cli.ResolveServer (env > cfg) directly, so under an active --profile /
// CREWSHIP_PROFILE it probed a *different* host than the one every other
// doctor check (and every authenticated command) actually dials. This locks
// that the wrapper now resolves the effective server — flag > profile > env
// > cfg — same as its sibling checks in this PR (checkServerReachable,
// runCheckCLIConfigServerScheme).
func TestRunCheckEpisodicRecallMode_HonorsActiveProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","episodic":"vector"}`))
	}))
	defer srv.Close()

	origServer, origProfile, origCfg := flagServer, flagProfile, cliCfg
	t.Cleanup(func() { flagServer, flagProfile, cliCfg = origServer, origProfile, origCfg })

	// Profile server must win over CREWSHIP_SERVER (profile > env), just
	// like every authenticated command — point env at a bogus, unreachable
	// host so the check would come back INFO (unreachable) if it were still
	// resolving via env>cfg instead of the active profile.
	t.Setenv("CREWSHIP_SERVER", "http://127.0.0.1:1")
	flagServer = ""
	flagProfile = "dev2"
	cliCfg = &cli.CLIConfig{
		Servers: map[string]*cli.ServerProfile{
			"dev2": {Server: srv.URL, Token: "t"},
		},
	}

	got := runCheckEpisodicRecallMode(context.Background())
	if got.status != "PASS" {
		t.Fatalf("active profile: status = %q, want PASS (detail=%q) — did the check dial the profile server?", got.status, got.detail)
	}
}
