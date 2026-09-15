package main

// Acceptance for `crewship issue list --counts`, driven through the BUILT
// BINARY against a REAL api.Router over a REAL migrated database.
//
// `GET /api/v1/issues?counts=1` (#2464) answers the per-status totals in an
// `X-Status-Counts` response header, computed under the SAME filter as the
// page. A header is invisible to `jq`, so the CLI has to lift it into its
// output — and only the real handler can prove the counts follow the
// filter rather than describe the whole workspace.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// issueCountsWorkspaceID is CUID-shaped so the CLI treats it as an already
// resolved workspace id and fires no slug→id round-trip.
const issueCountsWorkspaceID = "cisscountsws0000001a"

// startIssueCountsServer seeds one crew with four issues — two BACKLOG, one
// TODO, one DONE — and returns the CLI config path pointing at the router.
func startIssueCountsServer(t *testing.T) string {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Counts', 'counts-ws')`, issueCountsWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('ic-owner', 'owner@ic-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('icm-owner', ?, 'ic-owner', 'OWNER')`,
		issueCountsWorkspaceID)
	const ownerToken = "crewship_cli_icowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ic-owner', 'ic-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix, network_mode, container_memory_mb, container_cpus)
		VALUES ('ic-crew', ?, 'Engineering', 'engineering', 'ENG', 'free', 4096, 2.0)`, issueCountsWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('ic-lead', ?, 'ic-crew', 'Lead', 'lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`, issueCountsWorkspaceID)

	for i, status := range []string{"BACKLOG", "BACKLOG", "TODO", "DONE"} {
		mustExec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, number, identifier,
			priority, sort_order, mission_type, created_at, updated_at)
			VALUES (?, ?, 'ic-crew', 'ic-lead', ?, ?, ?, ?, ?, 'medium', 0, 'issue', datetime('now'), datetime('now'))`,
			"ic-issue-"+string(rune('a'+i)), issueCountsWorkspaceID, "trace-ic-"+string(rune('a'+i)),
			"Issue "+status, status, i+1, "ENG-"+string(rune('1'+i)))
	}

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + issueCountsWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runIssueCountsCLI(t *testing.T, cfgPath string, args ...string) string {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestAcceptance_IssueList_CountsFooterInTheTable(t *testing.T) {
	cfg := startIssueCountsServer(t)

	out := runIssueCountsCLI(t, cfg, "issue", "list", "--counts")
	for _, want := range []string{"ENG-1", "ENG-4", "BACKLOG 2", "TODO 1", "DONE 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("issue list --counts is missing %q:\n%s", want, out)
		}
	}
	// Order is the board's, not the map's: BACKLOG before TODO before DONE.
	if strings.Index(out, "BACKLOG 2") > strings.Index(out, "TODO 1") || strings.Index(out, "TODO 1") > strings.Index(out, "DONE 1") {
		t.Errorf("status counts should follow the workflow order:\n%s", out)
	}

	// Without the flag nothing about counts appears — the default listing
	// reads exactly as before.
	plain := runIssueCountsCLI(t, cfg, "issue", "list")
	if strings.Contains(plain, "BACKLOG 2") || strings.Contains(strings.ToLower(plain), "status counts") {
		t.Errorf("issue list without --counts should print no counts:\n%s", plain)
	}
}

func TestAcceptance_IssueList_CountsShareTheFilter(t *testing.T) {
	cfg := startIssueCountsServer(t)

	// The server computes the totals under the same WHERE as the page, so a
	// status filter narrows the counts too — and the machine shape carries
	// them next to the rows, since a header is invisible to jq.
	out := runIssueCountsCLI(t, cfg, "issue", "list", "--counts", "--status", "TODO", "-f", "json")
	var body struct {
		Issues       []map[string]any `json:"issues"`
		StatusCounts map[string]int   `json:"status_counts"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("decode --counts json: %v\n%s", err, out)
	}
	if len(body.Issues) != 1 {
		t.Errorf("issues = %d rows, want the one TODO issue", len(body.Issues))
	}
	if len(body.StatusCounts) != 1 || body.StatusCounts["TODO"] != 1 {
		t.Errorf("status_counts = %v, want {TODO: 1} — the counts must share the page's filter", body.StatusCounts)
	}

	// Opt-in only: without --counts the json body stays the bare array every
	// existing pipeline reads.
	plain := runIssueCountsCLI(t, cfg, "issue", "list", "-f", "json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(plain), &rows); err != nil {
		t.Fatalf("issue list -f json without --counts must stay a bare array: %v\n%s", err, plain)
	}
	if len(rows) != 4 {
		t.Errorf("bare array has %d rows, want 4", len(rows))
	}
}
