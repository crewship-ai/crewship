package backup_test

// A forked restore (--as-workspace / --as-crew) lands a COPY beside its
// source. Row counts and PRAGMA foreign_key_check say nothing about which
// workspace a new row belongs to, so #5575 passed every existing fork test:
// columns that are foreign keys in intent but bare TEXT in the schema kept the
// SOURCE's value. Some new rows were written into the source workspace (a
// duplicate notification channel, a second live automation), and some fork rows
// kept pointing at source rows (an agent's MCP binding, an issue's assignee,
// journal attribution).
//
// The two properties pinned here are about ownership, not integrity:
//
//  1. A fork never adds a row to the source workspace.
//  2. A fork row never references a row the bundle carried under its OLD id.

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// workspaceOwnedRowCounts counts, for every BackupTables entry that has a
// workspace_id column, the rows that column assigns to workspaceID.
func workspaceOwnedRowCounts(t *testing.T, db *sql.DB, workspaceID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range backup.BackupTables {
		if tableCreateSQL(t, db, table) == "" {
			continue
		}
		hasWorkspace := false
		for _, c := range tableGenColumns(t, db, table) {
			if strings.EqualFold(c.name, "workspace_id") {
				hasWorkspace = true
				break
			}
		}
		if !hasWorkspace {
			continue
		}
		var n int
		if err := db.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM `+table+` WHERE workspace_id = ?`, workspaceID).Scan(&n); err != nil {
			t.Fatalf("count %s owned by %s: %v", table, workspaceID, err)
		}
		out[table] = n
	}
	return out
}

// assertSourceUntouched fails with every table whose source-owned row count a
// fork changed.
func assertSourceUntouched(t *testing.T, before, after map[string]int) {
	t.Helper()
	var grew []string
	for table, n := range after {
		if n != before[table] {
			grew = append(grew, table+": "+strconv.Itoa(before[table])+" → "+strconv.Itoa(n))
		}
	}
	sort.Strings(grew)
	if len(grew) > 0 {
		t.Errorf("a forked restore changed rows the SOURCE workspace owns (#5575: a bare "+
			"TEXT workspace_id the remap did not follow — add it to virtualForeignKeys or "+
			"declare the FK):\n  %s", strings.Join(grew, "\n  "))
	}
}

func TestForkedRestore_SourceIsolationAndReferences(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	source := seedWorkspace(t, db)
	missionID, _, activityID := seedLiveMission(t, db, source)

	// The generator gives each row the id gen_<table> and points its declared
	// FKs at rows of this workspace.
	for _, table := range []string{
		"workspace_mcp_servers", "crew_mcp_servers",
		"notification_channels", "automations", "journal_entries",
	} {
		if err := seedOneBackupTable(t, db, table, source); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope) VALUES ('amb_ws', 'a_alice', 'gen_workspace_mcp_servers', 'workspace')`, nil},
		{`INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope) VALUES ('amb_crew', 'a_bob', 'gen_crew_mcp_servers', 'crew')`, nil},
		{`UPDATE missions SET assignee_type = 'agent', assignee_id = 'a_bob' WHERE id = ?`, []any{missionID}},
		{`UPDATE journal_entries SET crew_id = 'c_alpha', agent_id = 'a_alice', mission_id = ?, span_id = ? WHERE id = 'gen_journal_entries'`, []any{missionID, activityID}},
	} {
		if _, err := db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}

	before := workspaceOwnedRowCounts(t, db, source)

	const passphrase = "fork-source-isolation-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: source, OutputDir: t.TempDir(),
		Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	res, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path: created.Path, Passphrase: passphrase, Actor: actor, AsWorkspace: "e2e-ws-isolation-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	fork := res.RestoredWorkspaceID
	if fork == "" || fork == source {
		t.Fatalf("--as-workspace did not fork (got %q, source %q)", fork, source)
	}
	assertNoFKViolations(t, db, "after isolation fork")

	// 1. The source is exactly as it was.
	assertSourceUntouched(t, before, workspaceOwnedRowCounts(t, db, source))

	// The fork owns the copies the source had to lose them to before #5575.
	for _, table := range []string{"notification_channels", "automations"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE workspace_id = ?`, fork).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != before[table] {
			t.Errorf("fork owns %d %s row(s), want the source's %d", n, table, before[table])
		}
	}

	// 2. Every reference resolves inside the fork.
	for _, c := range []struct {
		what  string
		query string
	}{
		{"workspace MCP binding → fork's workspace server",
			`SELECT COUNT(*) FROM agent_mcp_bindings b JOIN agents a ON a.id = b.agent_id
			 WHERE a.workspace_id = ?1 AND b.mcp_server_scope = 'workspace'
			   AND b.mcp_server_id IN (SELECT id FROM workspace_mcp_servers WHERE workspace_id = ?1)`},
		{"crew MCP binding → fork's crew server",
			`SELECT COUNT(*) FROM agent_mcp_bindings b JOIN agents a ON a.id = b.agent_id
			 WHERE a.workspace_id = ?1 AND b.mcp_server_scope = 'crew'
			   AND b.mcp_server_id IN (SELECT s.id FROM crew_mcp_servers s JOIN crews c ON c.id = s.crew_id WHERE c.workspace_id = ?1)`},
		{"issue assignee → fork's agent",
			`SELECT COUNT(*) FROM missions m WHERE m.workspace_id = ?1 AND m.assignee_type = 'agent'
			   AND m.assignee_id IN (SELECT id FROM agents WHERE workspace_id = ?1)`},
		{"journal crew/agent/mission/span → fork's rows",
			`SELECT COUNT(*) FROM journal_entries j WHERE j.workspace_id = ?1
			   AND j.crew_id IN (SELECT id FROM crews WHERE workspace_id = ?1)
			   AND j.agent_id IN (SELECT id FROM agents WHERE workspace_id = ?1)
			   AND j.mission_id IN (SELECT id FROM missions WHERE workspace_id = ?1)
			   AND j.span_id IN (SELECT ma.id FROM mission_activity ma JOIN missions m ON m.id = ma.mission_id WHERE m.workspace_id = ?1)`},
	} {
		var n int
		if err := db.QueryRowContext(ctx, c.query, fork).Scan(&n); err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if n != 1 {
			t.Errorf("%s: %d matching fork row(s), want 1 — the fork row still names the source's row", c.what, n)
		}
	}
}
