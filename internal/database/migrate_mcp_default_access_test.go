package database

// #2072 — the migration that turns "who may use this MCP server" from an
// inference into a column, plus the referential integrity mcp_tool_bindings
// never had.
//
// The interesting half is the backfill. Defaulting every existing row to
// 'all' would be wrong in the widening direction: a server that already
// carries an agent binding is opt-in TODAY (resolution counted bindings and
// treated a non-zero count as "only the bound agents"), so leaving it open
// would hand it to every unbound agent on the first boot after the upgrade.
// The migration therefore freezes each server's CURRENT effective audience,
// and these tests are what says so.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// mcpDefaultAccessVersion is the migration under test. Kept as a literal so a
// renumber on merge has to come through this test too.
const mcpDefaultAccessVersion = 20260826190607

func mcpAccessDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := Open("file:" + filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db.DB
}

func mcpAccessExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// mcpAccessUnapply puts the schema back the way it looked before this
// migration — columns gone, triggers gone, ledger row gone — so a test can
// seed pre-migration rows and then run the real migration body over them.
// SQLite can DROP COLUMN here only because default_access deliberately has no
// CHECK constraint on it; see the migration for why that was the right call.
func mcpAccessUnapply(t *testing.T, db *sql.DB) {
	t.Helper()
	mcpAccessExec(t, db, `ALTER TABLE workspace_mcp_servers DROP COLUMN default_access`)
	mcpAccessExec(t, db, `ALTER TABLE crew_mcp_servers DROP COLUMN default_access`)
	mcpAccessExec(t, db, `DROP TRIGGER IF EXISTS trg_mcp_tool_binding_fk_check`)
	mcpAccessExec(t, db, `DROP TRIGGER IF EXISTS trg_mcp_tool_bindings_cascade_on_ws_server_delete`)
	mcpAccessExec(t, db, `DROP TRIGGER IF EXISTS trg_mcp_tool_bindings_cascade_on_crew_server_delete`)
	mcpAccessExec(t, db, `DELETE FROM _migrations WHERE version = ?`, mcpDefaultAccessVersion)
}

// mcpAccessSeedWorld seeds a workspace, a crew and an agent — the FK parents
// every row below needs.
func mcpAccessSeedWorld(t *testing.T, db *sql.DB) {
	t.Helper()
	mcpAccessExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','Work','work')`)
	mcpAccessExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, network_mode) VALUES ('crew1','ws1','Eng','eng','free')`)
	mcpAccessExec(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role, status)
		VALUES ('ag1','crew1','ws1','Pepa','pepa','AGENT','IDLE')`)
}

// TestMCPDefaultAccess_NewRowsAreOpen: a server created without naming an
// audience is available to every agent, which is what the old zero-binding
// inference meant and never stored. Asserted through a raw INSERT so it is
// the DB default doing the work, not a handler.
func TestMCPDefaultAccess_NewRowsAreOpen(t *testing.T) {
	db := mcpAccessDB(t, "open.db")
	mcpAccessSeedWorld(t, db)

	mcpAccessExec(t, db, `INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport)
		VALUES ('s1','ws1','github','GitHub','streamable-http')`)
	mcpAccessExec(t, db, `INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport)
		VALUES ('c1','crew1','jira','Jira','streamable-http')`)

	for _, tc := range []struct{ table, id string }{
		{"workspace_mcp_servers", "s1"},
		{"crew_mcp_servers", "c1"},
	} {
		var got string
		if err := db.QueryRow(`SELECT default_access FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&got); err != nil {
			t.Fatalf("%s.default_access: %v", tc.table, err)
		}
		if got != "all" {
			t.Errorf("%s: default_access = %q on an INSERT that omitted it, want \"all\"", tc.table, got)
		}
	}
}

// TestMCPDefaultAccess_BackfillFreezesTodaysAudience rebuilds the
// pre-migration schema (drop the columns, forget the ledger row), seeds the
// two states a live install can be in, and re-applies the real migration.
func TestMCPDefaultAccess_BackfillFreezesTodaysAudience(t *testing.T) {
	db := mcpAccessDB(t, "backfill.db")
	mcpAccessSeedWorld(t, db)

	// Back to the pre-migration shape.
	mcpAccessUnapply(t, db)

	cases := []struct {
		name    string
		table   string
		id      string
		scope   string
		bound   bool
		want    string
		because string
	}{
		{
			name: "workspace_unbound", table: "workspace_mcp_servers", id: "s-open", scope: "workspace",
			bound: false, want: "all",
			because: "nobody was bound, so every agent resolved it — that stays true",
		},
		{
			name: "workspace_bound", table: "workspace_mcp_servers", id: "s-bound", scope: "workspace",
			bound: true, want: "bound-only",
			because: "a binding already made it opt-in; 'all' here would GRANT it to every unbound agent on upgrade",
		},
		{
			name: "crew_unbound", table: "crew_mcp_servers", id: "c-open", scope: "crew",
			bound: false, want: "all",
			because: "crew-scoped servers follow the same rule",
		},
		{
			name: "crew_bound", table: "crew_mcp_servers", id: "c-bound", scope: "crew",
			bound: true, want: "bound-only",
			because: "crew-scoped servers follow the same rule",
		},
	}

	for _, tc := range cases {
		if tc.table == "workspace_mcp_servers" {
			mcpAccessExec(t, db, `INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport)
				VALUES (?, 'ws1', ?, ?, 'streamable-http')`, tc.id, tc.id, tc.id)
		} else {
			mcpAccessExec(t, db, `INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport)
				VALUES (?, 'crew1', ?, ?, 'streamable-http')`, tc.id, tc.id, tc.id)
		}
		if tc.bound {
			mcpAccessExec(t, db, `INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, enabled)
				VALUES (?, 'ag1', ?, ?, 1)`, "b-"+tc.id, tc.id, tc.scope)
		}
	}

	// Re-apply the migration for real.
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			if err := db.QueryRow(`SELECT default_access FROM `+tc.table+` WHERE id = ?`, tc.id).Scan(&got); err != nil {
				t.Fatalf("read default_access: %v", err)
			}
			if got != tc.want {
				t.Errorf("default_access = %q, want %q — %s", got, tc.want, tc.because)
			}
		})
	}
}

// TestMCPDefaultAccess_ToolBindingsFollowTheirServer covers the second half of
// the issue: mcp_tool_bindings has no FK (its server id is polymorphic across
// two tables, so it cannot have one), and the hand-written delete cascades in
// the handlers never mentioned it. Every per-tool toggle outlived its server.
func TestMCPDefaultAccess_ToolBindingsFollowTheirServer(t *testing.T) {
	db := mcpAccessDB(t, "toolbindings.db")
	mcpAccessSeedWorld(t, db)

	mcpAccessExec(t, db, `INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport)
		VALUES ('s1','ws1','github','GitHub','streamable-http')`)
	mcpAccessExec(t, db, `INSERT INTO crew_mcp_servers (id, crew_id, name, display_name, transport)
		VALUES ('c1','crew1','jira','Jira','streamable-http')`)
	mcpAccessExec(t, db, `INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
		VALUES ('t1','s1','workspace','create_issue',1)`)
	mcpAccessExec(t, db, `INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
		VALUES ('t2','c1','crew','create_ticket',1)`)

	mcpAccessExec(t, db, `DELETE FROM workspace_mcp_servers WHERE id = 's1'`)
	mcpAccessExec(t, db, `DELETE FROM crew_mcp_servers WHERE id = 'c1'`)

	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_bindings`).Scan(&left); err != nil {
		t.Fatalf("count tool bindings: %v", err)
	}
	if left != 0 {
		t.Errorf("%d tool bindings survived their server, want 0 — they are unreachable and would be "+
			"re-adopted by any row that reused the id", left)
	}
}

// TestMCPDefaultAccess_RejectsToolBindingForMissingServer is the insert half
// of the emulated FK, mirroring trg_agent_mcp_binding_fk_check from v30.
func TestMCPDefaultAccess_RejectsToolBindingForMissingServer(t *testing.T) {
	db := mcpAccessDB(t, "toolfk.db")
	mcpAccessSeedWorld(t, db)

	for _, scope := range []string{"workspace", "crew"} {
		if _, err := db.Exec(`INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
			VALUES (?, 'ghost', ?, 'do_thing', 1)`, "t-"+scope, scope); err == nil {
			t.Errorf("scope %s: insert naming a non-existent server succeeded, want ABORT", scope)
		}
	}
}

// TestMCPDefaultAccess_SweepsPreExistingOrphans: the rows already stranded by
// every integration deleted before this migration.
func TestMCPDefaultAccess_SweepsPreExistingOrphans(t *testing.T) {
	db := mcpAccessDB(t, "orphans.db")
	mcpAccessSeedWorld(t, db)

	// Plant the orphan the way history made it: on the pre-migration schema,
	// where nothing stopped a tool binding from naming a server that was
	// already gone.
	mcpAccessUnapply(t, db)
	mcpAccessExec(t, db, `INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
		VALUES ('orphan','deleted-server','workspace','create_issue',1)`)
	// A live row, to prove the sweep is not a truncate.
	mcpAccessExec(t, db, `INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport)
		VALUES ('s1','ws1','github','GitHub','streamable-http')`)
	mcpAccessExec(t, db, `INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, enabled)
		VALUES ('live','s1','workspace','create_issue',1)`)

	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}

	var orphans, live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_bindings WHERE id = 'orphan'`).Scan(&orphans); err != nil {
		t.Fatalf("count orphan: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM mcp_tool_bindings WHERE id = 'live'`).Scan(&live); err != nil {
		t.Fatalf("count live: %v", err)
	}
	if orphans != 0 {
		t.Error("the pre-existing orphan survived the sweep")
	}
	if live != 1 {
		t.Error("the sweep removed a tool binding whose server still exists")
	}
}
