package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestKeeperHistoryCmdRegistered(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["history"] {
		t.Fatalf("keeper missing subcommand %q; have %v", "history", have)
	}
	if keeperHistoryCmd.Args == nil {
		t.Error("keeper history must require exactly one request id")
	}
}

// TestKeeperHistoryRunE_HitsEventsRoute pins the API↔CLI parity contract: the
// command must consume the events endpoint (not re-derive history from the
// current-state log) and must path-escape the request id.
func TestKeeperHistoryRunE_HitsEventsRoute(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// EscapedPath, not Path: Path is already percent-decoded, so it cannot
		// show whether the client escaped the id before putting it in the URL.
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"seq": 1, "state": "PENDING", "actor_type": "agent", "recorded_at": "2026-07-25T10:00:00Z"},
			{"seq": 2, "state": "ALLOW", "actor_type": "keeper", "risk_score": 2, "exit_code": 0,
				"reason": "looks fine", "recorded_at": "2026-07-25T10:00:05Z"},
		})
	}))
	defer srv.Close()
	withKeeperTestServer(t, srv.URL)

	if err := keeperHistoryCmd.RunE(keeperHistoryCmd, []string{"kpr_exe_abc/def"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	want := "/api/v1/admin/keeper/requests/kpr_exe_abc%2Fdef/events"
	if gotPath != want {
		t.Errorf("requested %q, want %q (the id must be path-escaped)", gotPath, want)
	}
}

// TestKeeperHistoryRunE_EmptyIsNotAnError: an empty ledger is a legitimate answer
// for a pre-migration request, and — deliberately — also what a foreign-workspace
// id returns. The command must not turn that into a failure or claim the request
// does not exist.
func TestKeeperHistoryRunE_EmptyIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	withKeeperTestServer(t, srv.URL)

	if err := keeperHistoryCmd.RunE(keeperHistoryCmd, []string{"kpr_none"}); err != nil {
		t.Fatalf("an empty ledger must not be an error: %v", err)
	}
}

// TestKeeperHistoryRunE_SurfacesPermissionHint: the route is ADMIN+, so a 403 must
// come back as the role-specific hint the other keeper commands use rather than a
// bare HTTP error.
func TestKeeperHistoryRunE_SurfacesPermissionHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Forbidden: ADMIN or OWNER only"}`))
	}))
	defer srv.Close()
	withKeeperTestServer(t, srv.URL)

	err := keeperHistoryCmd.RunE(keeperHistoryCmd, []string{"kpr_x"})
	if err == nil {
		t.Fatal("expected a 403 to surface as an error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "admin") &&
		!strings.Contains(strings.ToLower(err.Error()), "owner") {
		t.Errorf("error %q should mention the required role", err)
	}
}

// withKeeperTestServer points the CLI at a stub server, reusing the same
// saveCLIState restore hook the governance tests use so the global cliCfg is put
// back afterwards.
func withKeeperTestServer(t *testing.T, baseURL string) {
	t.Helper()
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    baseURL,
	}
}
