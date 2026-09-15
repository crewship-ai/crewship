package main

// Acceptance for `crewship journal --agent/--crew` reference resolution,
// driven through the BUILT BINARY against a REAL api.Router over a REAL
// migrated database.
//
// #2216 made the server accept what a person types — an id, a slug, or the
// display name — for both filters (internal/api/journal_handler.go
// resolveJournalRefs), and the CLI help says so: "Filter by crew name, slug
// or ID". The CLI change was help-text only and had no test driving the
// binary, which is how a display name kept working for --agent and failing
// for --crew: the crew flag went through the CLI's own slug-only resolver
// and never reached the server that knows about names.
//
// So every spelling is tried here for both flags, against the real
// resolver, and the assertion is on which rows came back.

import (
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

// journalRefsWorkspaceID is CUID-shaped so the CLI treats it as an already
// resolved workspace id and fires no slug→id round-trip.
const journalRefsWorkspaceID = "cjrnlrefsws00000001a"

// startJournalRefsServer builds the real router over a migrated DB holding
// two crews (Backend / Growth), one agent in each (Viktor / Nadia), and one
// journal entry per agent whose summary names it.
func startJournalRefsServer(t *testing.T) string {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Journal', 'journal-ws')`, journalRefsWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('jr-owner', 'owner@jr-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('jrm-owner', ?, 'jr-owner', 'OWNER')`,
		journalRefsWorkspaceID)
	const ownerToken = "crewship_cli_jrowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-jr-owner', 'jr-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	for _, c := range [][2]string{{"jr-crew-backend", "Backend"}, {"jr-crew-growth", "Growth"}} {
		mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
			VALUES (?, ?, ?, ?, 'free', 4096, 2.0)`, c[0], journalRefsWorkspaceID, c[1], strings.ToLower(c[1]))
	}
	for _, a := range [][3]string{{"jr-agent-viktor", "Viktor", "jr-crew-backend"}, {"jr-agent-nadia", "Nadia", "jr-crew-growth"}} {
		mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled)
			VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
			a[0], journalRefsWorkspaceID, a[2], a[1], strings.ToLower(a[1]))
	}
	entry := func(id, agentID, crewID, summary string) {
		mustExec(`INSERT INTO journal_entries
				(id, workspace_id, crew_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs)
			VALUES (?, ?, ?, ?, '2026-09-15T10:00:00.000Z', 'agent.status', 'info', 'normal', 'agent', ?, ?, '{}', '{}')`,
			id, journalRefsWorkspaceID, crewID, agentID, agentID, summary)
	}
	entry("jr-entry-v", "jr-agent-viktor", "jr-crew-backend", "VIKTOR-ROW Viktor finished the backend deploy")
	entry("jr-entry-n", "jr-agent-nadia", "jr-crew-growth", "NADIA-ROW Nadia drafted the growth digest")

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + journalRefsWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runJournalRefsCLI(t *testing.T, cfgPath string, args ...string) string {
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

// assertOnlyViktor pins the filter: Viktor's row is there, Nadia's is not.
func assertOnlyViktor(t *testing.T, label, out string) {
	t.Helper()
	if !strings.Contains(out, "VIKTOR-ROW") {
		t.Errorf("%s: Viktor's entry is missing:\n%s", label, out)
	}
	if strings.Contains(out, "NADIA-ROW") {
		t.Errorf("%s: Nadia's entry leaked through the filter:\n%s", label, out)
	}
}

func TestAcceptance_Journal_AgentFilterAcceptsIdSlugAndName(t *testing.T) {
	cfg := startJournalRefsServer(t)
	for _, ref := range []string{"jr-agent-viktor", "viktor", "Viktor"} {
		assertOnlyViktor(t, "--agent "+ref, runJournalRefsCLI(t, cfg, "journal", "--agent", ref))
	}
}

func TestAcceptance_Journal_CrewFilterAcceptsIdSlugAndName(t *testing.T) {
	cfg := startJournalRefsServer(t)
	// The display name is the case that used to fail: the CLI resolved
	// --crew itself, by slug only, and answered "crew not found" for a
	// name the server resolves fine.
	for _, ref := range []string{"jr-crew-backend", "backend", "Backend"} {
		assertOnlyViktor(t, "--crew "+ref, runJournalRefsCLI(t, cfg, "journal", "--crew", ref))
	}
}

func TestAcceptance_JournalCount_ResolvesTheSameReferences(t *testing.T) {
	cfg := startJournalRefsServer(t)
	for _, args := range [][]string{
		{"journal", "count", "--agent", "Viktor"},
		{"journal", "count", "--crew", "Backend"},
		{"journal", "count", "--crew", "backend", "--agent", "viktor"},
	} {
		out := strings.TrimSpace(runJournalRefsCLI(t, cfg, args...))
		if !strings.HasSuffix(out, "1") {
			t.Errorf("%v = %q, want a count of 1", args, out)
		}
	}
}

func TestAcceptance_Journal_UnknownCrewFiltersToNothingNotAnError(t *testing.T) {
	// The branch the fix changed. Before, an unknown --crew was refused by
	// the CLI's own slug-only resolver ("crew not found", exit 3); now it
	// reaches the server, which leaves an unresolved reference as typed so
	// it filters on the id column and matches nothing — the documented
	// rule ("a typo returns nothing rather than silently widening"). Exit
	// 0 with zero rows is the contract, the same one --agent has had.
	cfg := startJournalRefsServer(t)

	out := runJournalRefsCLI(t, cfg, "journal", "--crew", "nobody")
	if strings.Contains(out, "VIKTOR-ROW") || strings.Contains(out, "NADIA-ROW") {
		t.Errorf("--crew nobody must match no rows (not widen to the workspace):\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("--crew nobody must not be refused client-side:\n%s", out)
	}

	count := strings.TrimSpace(runJournalRefsCLI(t, cfg, "journal", "count", "--crew", "nobody"))
	if !strings.HasSuffix(count, "0") {
		t.Errorf("journal count --crew nobody = %q, want 0", count)
	}
}
