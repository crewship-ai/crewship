package backup_test

// The round trip that proves #1973 was data loss and not a lint finding.
//
// DiscoverScopedTables walks reverse foreign keys and keeps the SHORTEST path
// to a workspace-scoped table. Nothing in that walk used to ask whether the
// column it chose can be NULL — and a filter on a nullable column omits every
// row where it is. The backup succeeds, the bundle verifies, and the rows are
// simply not in it.
//
// So this test does not inspect a WHERE clause. It seeds the ordinary shape of
// each affected table — a task nobody has claimed, a crew-local MCP server, a
// crew's issue counter — takes a real bundle through CreateBackup, restores it
// into a fresh instance with RestoreBackup, and asserts the rows came back.
//
// On the pre-fix walk this failed with zero mission_tasks and zero
// crew_mcp_servers restored, because the filters were
//
//	mission_tasks:    "assignment_id" IN (SELECT id FROM assignments WHERE workspace_id = ?)
//	crew_mcp_servers: "workspace_mcp_server_id" IN (SELECT id FROM workspace_mcp_servers WHERE workspace_id = ?)
//
// and both of those columns are NULL for the rows a normal workspace holds.

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
)

// seedNullableScopeWorkspace inserts a workspace whose rows are the ORDINARY
// ones: every nullable FK that the reverse-FK walk found attractive is left
// NULL, because that is what it is in production for these tables.
func seedNullableScopeWorkspace(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	const (
		workspaceID = "ws_nullfk"
		crewID      = "c_nullfk"
		agentID     = "a_nullfk"
		missionID   = "m_nullfk"
	)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}

	exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		"u_nullfk", "nullfk@e2e.test", "Null FK")
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		workspaceID, "Nullable FK Workspace", "nullfk-ws")
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
		crewID, workspaceID, "Null FK Crew", "nullfk-crew")
	exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status)
	      VALUES (?, ?, ?, ?, ?, 'LEAD', 'IDLE')`,
		agentID, crewID, workspaceID, "Lead", "nullfk-lead")

	// mission_tasks. assigned_agent_id is NULL until somebody claims the task
	// and assignment_id is NULL until the orchestrator queues it — which is
	// the state every task is created in.
	exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
	      VALUES (?, ?, ?, ?, 'tr_nullfk', 'Mission', 'IN_PROGRESS', datetime('now'))`,
		missionID, workspaceID, crewID, agentID)
	exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
	      VALUES ('t_unclaimed', ?, 'Unclaimed task', 'PENDING', 1, datetime('now'), datetime('now'))`,
		missionID)

	// crew_mcp_servers. workspace_mcp_server_id is NULL for a server a crew
	// configured for itself rather than adopting from the workspace catalogue.
	exec(`INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint)
	      VALUES ('mcp_crewlocal', ?, 'crew-local', 'Crew Local', 'streamable-http', 'https://mcp.example/sse')`,
		crewID)

	// issue_counters. crew_id is the primary key and every writer supplies it;
	// the counter is what makes restored issue identifiers continue rather
	// than restart at 1 and collide with history.
	exec(`INSERT INTO issue_counters (crew_id, next_number) VALUES (?, 42)`, crewID)

	return workspaceID
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// TestE2E_NullableScopeColumns_RowsSurviveRoundTrip is the data-level proof for
// #1973: back up a workspace whose rows hold NULL in the column the FK walk
// picked, restore into a fresh instance, and require the rows to be there.
func TestE2E_NullableScopeColumns_RowsSurviveRoundTrip(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedNullableScopeWorkspace(t, source)

	bundleDir := t.TempDir()
	createResult, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   bundleDir,
		Actor: backup.Actor{
			UserID: "u_nullfk",
			Email:  "nullfk@e2e.test",
			Role:   "ADMIN",
		},
		NoEncrypt: true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	if _, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path: createResult.Path,
		Actor: backup.Actor{
			UserID: "u_nullfk",
			Email:  "nullfk@e2e.test",
			Role:   "ADMIN",
		},
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// mission_tasks — scoped through mission_id (NOT NULL), not through the
	// agent who has not claimed it or the assignment that does not exist yet.
	if n := countRows(t, target,
		`SELECT COUNT(*) FROM mission_tasks WHERE mission_id IN
		   (SELECT id FROM missions WHERE workspace_id = ?)`, workspaceID); n != 1 {
		t.Errorf("mission_tasks after restore = %d, want 1 "+
			"(an unclaimed task has assigned_agent_id AND assignment_id NULL; "+
			"scoping through either omits it from the bundle)", n)
	}

	// crew_mcp_servers — scoped through crew_id (NOT NULL), not through the
	// optional workspace catalogue entry it was adopted from.
	if n := countRows(t, target,
		`SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id IN
		   (SELECT id FROM crews WHERE workspace_id = ?)`, workspaceID); n != 1 {
		t.Errorf("crew_mcp_servers after restore = %d, want 1 "+
			"(a crew-local server has workspace_mcp_server_id NULL; "+
			"scoping through it omits the row from the bundle)", n)
	}

	// issue_counters — the counter itself must come back, or restored crews
	// re-issue identifiers they already used.
	var next int
	if err := target.QueryRowContext(ctx,
		`SELECT next_number FROM issue_counters WHERE crew_id = 'c_nullfk'`).Scan(&next); err != nil {
		t.Fatalf("issue_counters after restore: %v", err)
	}
	if next != 42 {
		t.Errorf("issue_counters.next_number = %d, want 42", next)
	}
}
