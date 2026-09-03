package main

// #2303 acceptance — `crew list`, `agent list` and `credential list` driven
// through the BUILT BINARY against the REAL api router, on a workspace that
// holds more rows than the page.
//
// The bug this pins: the API windowed every list at 100 rows and said
// nothing about it, so the CLI printed a table one row short of the truth
// and called it the workspace. A stub server cannot catch that — it answers
// whatever the test hands it — so the server here is api.NewRouter over a
// migrated database with rows seeded straight into it.

import (
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// fleetPagingWorkspaceID is CUID-shaped so the CLI treats it as an already
// resolved workspace id and fires no slug→id round-trip.
const fleetPagingWorkspaceID = "cpagingws00000000001a"

// startFleetPagingServer builds the real router over a migrated SQLite DB holding
// one workspace, one OWNER with a CLI token, three crews, four agents and
// three credentials, and writes a CLI config pointing at it.
func startFleetPagingServer(t *testing.T) string {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Paging', 'paging-ws')`, fleetPagingWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('pg-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('pgm-owner', ?, 'pg-owner', 'OWNER')`,
		fleetPagingWorkspaceID)
	const ownerToken = "crewship_cli_pgowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-pg-owner', 'pg-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("crew-%d", i)
		mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus, created_at)
			VALUES (?, ?, ?, ?, 'free', 4096, 2.0, ?)`, id, fleetPagingWorkspaceID, fmt.Sprintf("Crew %d", i), id, fmt.Sprintf("2026-01-01T00:00:0%dZ", i))
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("agent-%d", i)
		crew := "crew-1"
		if i == 4 {
			crew = "crew-2"
		}
		mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled, created_at)
			VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0, ?)`,
			id, fleetPagingWorkspaceID, crew, "Agent "+id, id, fmt.Sprintf("2026-01-01T00:00:0%dZ", i))
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("cred-%d", i)
		mustExec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
			VALUES (?, ?, ?, 'x', 'API_KEY', 'GITHUB', 'WORKSPACE', 'ACTIVE', 'pg-owner', ?, ?)`,
			id, fleetPagingWorkspaceID, strings.ToUpper(strings.ReplaceAll(id, "-", "_")),
			fmt.Sprintf("2026-01-01 00:00:0%d", i), fmt.Sprintf("2026-01-01 00:00:0%d", i))
	}

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + fleetPagingWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// runFleetPagingCLI runs the built binary and returns stdout and stderr
// separately: the table and its "showing a–b of N" footer are stdout (human
// formats only), the credential cursor hint is stderr.
func runFleetPagingCLI(t *testing.T, cfgPath string, args ...string) (stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v failed: %v\nstdout:\n%s\nstderr:\n%s", args, err, out.String(), errb.String())
	}
	return out.String(), errb.String()
}

// countTableRows counts table rows whose first cell matches the id pattern — the
// header, the box-drawing rules and the note never do.
func countTableRows(out string, pattern string) int {
	return len(regexp.MustCompile(pattern).FindAllString(out, -1))
}

func TestAcceptance_ListPaging_CrewList(t *testing.T) {
	cfg := startFleetPagingServer(t)

	stdout, stderr := runFleetPagingCLI(t, cfg, "crew", "list", "--limit", "2")
	if n := countTableRows(stdout, `crew-\d`); n != 2 {
		t.Errorf("--limit 2 printed %d crews, want 2:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "showing 1–2 of 3 · next page: --offset 2") {
		t.Errorf("the footer should say the table is a window on 3 crews, got:\n%s\nstderr:\n%s", stdout, stderr)
	}

	stdout, _ = runFleetPagingCLI(t, cfg, "crew", "list", "--limit", "2", "--offset", "2")
	if n := countTableRows(stdout, `crew-\d`); n != 1 {
		t.Errorf("--offset 2 printed %d crews, want 1:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "showing 3–3 of 3") {
		t.Errorf("the footer should place the window, got:\n%s", stdout)
	}

	// Everything on one page: no note at all — the common case reads as before.
	stdout, stderr = runFleetPagingCLI(t, cfg, "crew", "list")
	if n := countTableRows(stdout, `crew-\d`); n != 3 {
		t.Errorf("default page printed %d crews, want 3:\n%s", n, stdout)
	}
	if strings.Contains(stdout, "showing") || stderr != "" {
		t.Errorf("a complete table must print no footer, got:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// Machine formats never get the footer: scripts read the array.
	stdout, _ = runFleetPagingCLI(t, cfg, "crew", "list", "--limit", "2", "-f", "json")
	if strings.Contains(stdout, "showing") {
		t.Errorf("json output must carry no footer:\n%s", stdout)
	}
}

func TestAcceptance_ListPaging_AgentList(t *testing.T) {
	cfg := startFleetPagingServer(t)

	stdout, stderr := runFleetPagingCLI(t, cfg, "agent", "list", "--limit", "3")
	if n := countTableRows(stdout, `agent-\d`); n != 3 {
		t.Errorf("--limit 3 printed %d agents, want 3:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "showing 1–3 of 4 · next page: --offset 3") {
		t.Errorf("the footer should say the table is a window on 4 agents, got:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// The crew filter narrows the total the note reports.
	stdout, stderr = runFleetPagingCLI(t, cfg, "agent", "list", "--crew", "crew-1", "--limit", "2")
	if n := countTableRows(stdout, `agent-\d`); n != 2 {
		t.Errorf("--crew crew-1 --limit 2 printed %d agents, want 2:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "showing 1–2 of 3") {
		t.Errorf("the footer should count only crew-1's agents, got:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestAcceptance_ListPaging_CredentialList(t *testing.T) {
	cfg := startFleetPagingServer(t)

	// --offset selects the positional window; the note comes from the headers.
	stdout, stderr := runFleetPagingCLI(t, cfg, "credential", "list", "--limit", "2", "--offset", "1")
	if n := countTableRows(stdout, `CRED_\d`); n != 2 {
		t.Errorf("--limit 2 --offset 1 printed %d credentials, want 2:\n%s", n, stdout)
	}
	if !strings.Contains(stdout, "showing 2–3 of 3") {
		t.Errorf("the footer should describe the window, got:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// The older cursor mode keeps its own hint and still knows the total.
	stdout, stderr = runFleetPagingCLI(t, cfg, "credential", "list", "--limit", "2")
	if n := countTableRows(stdout, `CRED_\d`); n != 2 {
		t.Errorf("--limit 2 printed %d credentials, want 2:\n%s", n, stdout)
	}
	if !strings.Contains(stderr, "More results available") {
		t.Errorf("cursor mode should keep its --cursor hint, got:\n%s", stderr)
	}
}

func TestAcceptance_ListPaging_RefusesOffsetWithCursor(t *testing.T) {
	cfg := startFleetPagingServer(t)
	cmd := exec.Command(buildCrewshipBinary(t), "credential", "list", "--offset", "1", "--all")
	cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("--offset with --all should be refused, got success:\n%s", out)
	}
	if !strings.Contains(string(out), "--offset") {
		t.Errorf("the refusal should name the flag, got:\n%s", out)
	}
}
