package backup

// Coverage tests for dbdump.go — DumpCrew / DumpWorkspace branch
// coverage, RestoreDumpTxHooks lifecycle hooks + FK enforcement,
// tombstone purge, and the marshal helpers.

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDumpCrew_Branches(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown crew", func(t *testing.T) {
		db := openMigratedDBCov(t)
		_, err := DumpCrew(ctx, db, "c_ghost")
		if err == nil || !strings.Contains(err.Error(), "resolve crew workspace") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("seeded crew dumps its slice", func(t *testing.T) {
		db := openMigratedDBCov(t)
		wsID, crewID := seedCovWorkspace(t, db, "dumpcrew")
		dump, err := DumpCrew(ctx, db, crewID)
		if err != nil {
			t.Fatalf("DumpCrew: %v", err)
		}
		if dump.WorkspaceID != wsID {
			t.Errorf("workspace id = %q, want %q", dump.WorkspaceID, wsID)
		}
		if n := len(dump.Tables["workspaces"]); n != 1 {
			t.Errorf("workspaces rows = %d", n)
		}
		if n := len(dump.Tables["crews"]); n != 1 {
			t.Errorf("crews rows = %d", n)
		}
		if n := len(dump.Tables["agents"]); n != 1 {
			t.Errorf("agents rows = %d", n)
		}
		if got := dump.Tables["crews"][0]["id"]; got != crewID {
			t.Errorf("crew id = %v", got)
		}
		// No skills bound to this crew's agents → empty/no rows but no error.
		if n := len(dump.Tables["skills"]); n != 0 {
			t.Errorf("unexpected skills rows: %v", dump.Tables["skills"])
		}
	})
}

func TestDumpWorkspace_MinimalSchemaSkips(t *testing.T) {
	ctx := context.Background()
	// A schema with only the anchor + one child: every other
	// BackupTables entry must be skipped silently, including `users`
	// whose scoping subqueries depend on absent tables.
	db := newRunnerCovDB(t, `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT, name TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id), slug TEXT, name TEXT);
		CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT);
		INSERT INTO workspaces VALUES ('ws1','slug-1','WS One');
		INSERT INTO workspaces VALUES ('ws2','slug-2','WS Two');
		INSERT INTO crews VALUES ('c1','ws1','crew-1','Crew One');
		INSERT INTO crews VALUES ('c2','ws2','crew-2','Crew Two');
	`)
	dump, err := DumpWorkspace(ctx, db, "ws1")
	if err != nil {
		t.Fatalf("DumpWorkspace: %v", err)
	}
	if len(dump.Tables["workspaces"]) != 1 || dump.Tables["workspaces"][0]["id"] != "ws1" {
		t.Errorf("workspaces = %v", dump.Tables["workspaces"])
	}
	if len(dump.Tables["crews"]) != 1 || dump.Tables["crews"][0]["id"] != "c1" {
		t.Errorf("crews must be scoped to ws1: %v", dump.Tables["crews"])
	}
	// users requires crew_members/chats/agent_skills/skills — absent →
	// the table is skipped wholesale, not dumped unscoped.
	if _, ok := dump.Tables["users"]; ok {
		t.Errorf("users must be skipped when scoping deps are missing: %v", dump.Tables["users"])
	}
	if _, ok := dump.Tables["agents"]; ok {
		t.Errorf("absent tables must not appear in the dump")
	}
}

// restoreCovSchema is a minimal FK-enforced schema covering two
// BackupTables entries with the id+deleted_at shape the tombstone
// purge requires.
const restoreCovSchema = `
	CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT, name TEXT, deleted_at TEXT);
	CREATE TABLE crews (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL REFERENCES workspaces(id),
		slug TEXT, deleted_at TEXT
	);
`

func TestRestoreDumpTxHooks_Lifecycle(t *testing.T) {
	ctx := context.Background()

	dump := &DBDump{
		WorkspaceID: "ws1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws1", "slug": "s", "name": "N"}},
			"crews":      {{"id": "c1", "workspace_id": "ws1", "slug": "c"}},
		},
	}

	t.Run("hooks run in order and rows land", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		var calls []string
		stats, err := RestoreDumpTxHooks(ctx, db, dump, &RestoreDumpHooks{
			PreInsert: func(ctx context.Context, tx *sql.Tx) error {
				calls = append(calls, "pre-insert")
				return nil
			},
			PreCommit: func(context.Context) error {
				calls = append(calls, "pre-commit")
				return nil
			},
		})
		if err != nil {
			t.Fatalf("RestoreDumpTxHooks: %v", err)
		}
		if stats.RowsSeen != 2 || stats.RowsInserted != 2 {
			t.Errorf("stats = %+v", stats)
		}
		if strings.Join(calls, ",") != "pre-insert,pre-commit" {
			t.Errorf("hook order = %v", calls)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM crews`).Scan(&n); err != nil || n != 1 {
			t.Errorf("crews rows = %d (%v)", n, err)
		}
	})

	t.Run("pre-insert failure rolls back", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		_, err := RestoreDumpTxHooks(ctx, db, dump, &RestoreDumpHooks{
			PreInsert: func(context.Context, *sql.Tx) error { return errors.New("wipe failed") },
		})
		if err == nil || !strings.Contains(err.Error(), "pre-insert hook") {
			t.Fatalf("err = %v", err)
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n)
		if n != 0 {
			t.Errorf("rollback failed, %d rows", n)
		}
	})

	t.Run("pre-commit failure rolls back inserts", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		_, err := RestoreDumpTxHooks(ctx, db, dump, &RestoreDumpHooks{
			PreCommit: func(context.Context) error { return errors.New("docker phase died") },
		})
		if err == nil || !strings.Contains(err.Error(), "pre-commit hook") {
			t.Fatalf("err = %v", err)
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n)
		if n != 0 {
			t.Errorf("rollback failed, %d rows", n)
		}
	})

	t.Run("nil hooks accepted", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		stats, err := RestoreDumpTxHooks(ctx, db, dump, nil)
		if err != nil || stats.RowsInserted != 2 {
			t.Fatalf("stats = %+v err = %v", stats, err)
		}
	})

	t.Run("unknown tables and unknown columns are skipped", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		odd := &DBDump{
			WorkspaceID: "ws1",
			Tables: map[string][]map[string]any{
				"workspaces": {{"id": "ws1", "slug": "s", "smuggled_column": "x"}},
				// `agents` is in BackupTables but absent from this schema.
				"agents": {{"id": "a1"}},
				// Row whose every column is unknown → zero-col row skipped.
				"crews": {{"only_unknown": 1}},
			},
		}
		stats, err := RestoreDumpTxHooks(ctx, db, odd, nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if stats.RowsSeen != 3 || stats.RowsInserted != 1 {
			t.Errorf("stats = %+v, want seen=3 inserted=1", stats)
		}
		var smuggled int
		// The unknown column must not have landed anywhere.
		row := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('workspaces') WHERE name='smuggled_column'`)
		if err := row.Scan(&smuggled); err != nil || smuggled != 0 {
			t.Errorf("smuggled column present? %d (%v)", smuggled, err)
		}
	})

	t.Run("tombstone with colliding PK is purged then re-inserted", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema+`
			INSERT INTO workspaces (id, slug, name, deleted_at) VALUES ('ws1','old','Old','2026-01-01');
		`)
		stats, err := RestoreDumpTxHooks(ctx, db, &DBDump{
			WorkspaceID: "ws1",
			Tables: map[string][]map[string]any{
				"workspaces": {{"id": "ws1", "slug": "s", "name": "Fresh"}},
			},
		}, nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if stats.RowsInserted != 1 {
			t.Errorf("stats = %+v", stats)
		}
		var name string
		var deletedAt sql.NullString
		if err := db.QueryRow(`SELECT name, deleted_at FROM workspaces WHERE id='ws1'`).Scan(&name, &deletedAt); err != nil {
			t.Fatal(err)
		}
		if name != "Fresh" || deletedAt.Valid {
			t.Errorf("tombstone not replaced: name=%q deleted_at=%v", name, deletedAt)
		}
	})

	t.Run("live row with colliding PK is preserved (INSERT OR IGNORE)", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema+`
			INSERT INTO workspaces (id, slug, name) VALUES ('ws1','live','Live');
		`)
		stats, err := RestoreDumpTxHooks(ctx, db, &DBDump{
			WorkspaceID: "ws1",
			Tables: map[string][]map[string]any{
				"workspaces": {{"id": "ws1", "slug": "s", "name": "FromBundle"}},
			},
		}, nil)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if stats.RowsSeen != 1 || stats.RowsInserted != 0 {
			t.Errorf("stats = %+v", stats)
		}
		var name string
		_ = db.QueryRow(`SELECT name FROM workspaces WHERE id='ws1'`).Scan(&name)
		if name != "Live" {
			t.Errorf("live row clobbered: %q", name)
		}
	})

	t.Run("orphan FK row aborts before commit", func(t *testing.T) {
		db := newRunnerCovDB(t, restoreCovSchema)
		preCommitRan := false
		_, err := RestoreDumpTxHooks(ctx, db, &DBDump{
			WorkspaceID: "ws1",
			Tables: map[string][]map[string]any{
				"crews": {{"id": "c1", "workspace_id": "ws_missing", "slug": "x"}},
			},
		}, &RestoreDumpHooks{
			PreCommit: func(context.Context) error { preCommitRan = true; return nil },
		})
		if err == nil || !strings.Contains(err.Error(), "deferred FK violations") {
			t.Fatalf("err = %v", err)
		}
		if preCommitRan {
			t.Error("pre-commit (docker phase) must NOT run when FKs are broken")
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM crews`).Scan(&n)
		if n != 0 {
			t.Errorf("orphan row committed: %d", n)
		}
	})
}

func TestRestoreDump_PlainWrapper(t *testing.T) {
	db := newRunnerCovDB(t, restoreCovSchema)
	err := RestoreDump(context.Background(), db, &DBDump{
		WorkspaceID: "ws1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws1", "slug": "s", "name": "N"}},
		},
	})
	if err != nil {
		t.Fatalf("RestoreDump: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&n)
	if n != 1 {
		t.Errorf("rows = %d", n)
	}
}

func TestAssertNoFKViolationsTx(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, restoreCovSchema)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	// Clean state → nil.
	if err := assertNoFKViolationsTx(ctx, tx); err != nil {
		t.Fatalf("clean tx: %v", err)
	}
	// Orphan child under deferred FKs → typed violation report.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO crews (id, workspace_id, slug) VALUES ('c_orphan', 'ws_void', 'x')`); err != nil {
		t.Fatal(err)
	}
	err = assertNoFKViolationsTx(ctx, tx)
	if err == nil || !strings.Contains(err.Error(), "deferred FK violations") || !strings.Contains(err.Error(), "crews") {
		t.Fatalf("err = %v", err)
	}
}

func TestDumpCrew_MinimalSchemaSkipsAbsentTables(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, `
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT, name TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT, slug TEXT, name TEXT);
		INSERT INTO workspaces VALUES ('ws1','s','N');
		INSERT INTO crews VALUES ('c1','ws1','crew-min','C');
	`)
	dump, err := DumpCrew(ctx, db, "c1")
	if err != nil {
		t.Fatalf("DumpCrew: %v", err)
	}
	if len(dump.Tables["crews"]) != 1 || len(dump.Tables["workspaces"]) != 1 {
		t.Errorf("tables = %v", dump.Tables)
	}
	for _, absent := range []string{"agents", "users", "skills", "chats", "journal_entries"} {
		if _, ok := dump.Tables[absent]; ok {
			t.Errorf("absent table %s must be skipped", absent)
		}
	}
}

func TestTableProbes_DeadTxErrors(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, restoreCovSchema)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Every probe against a finished tx must surface the driver error
	// rather than a bogus false/empty success.
	if _, err := tableExists(ctx, tx, "workspaces"); err == nil {
		t.Error("tableExists on dead tx must error")
	}
	if _, err := tableExistsTx(ctx, tx, "workspaces"); err == nil {
		t.Error("tableExistsTx on dead tx must error")
	}
	if _, err := tableHasColumn(ctx, tx, "workspaces", "id"); err == nil {
		t.Error("tableHasColumn on dead tx must error")
	}
	if _, err := tableColumns(ctx, tx, "workspaces"); err == nil {
		t.Error("tableColumns on dead tx must error")
	}
}

func TestDumpWorkspace_ClosedDB(t *testing.T) {
	db := newRunnerCovDB(t, restoreCovSchema)
	_ = db.Close()
	_, err := DumpWorkspace(context.Background(), db, "ws1")
	if err == nil || !strings.Contains(err.Error(), "begin dump tx") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreDumpTxHooks_ClosedDB(t *testing.T) {
	db := newRunnerCovDB(t, restoreCovSchema)
	_ = db.Close()
	_, err := RestoreDumpTxHooks(context.Background(), db, &DBDump{
		Tables: map[string][]map[string]any{"workspaces": {{"id": "w"}}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "acquire conn") {
		t.Fatalf("err = %v", err)
	}
}

func TestMarshalDump(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		d := &DBDump{WorkspaceID: "ws1", Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws1"}},
		}}
		raw, err := MarshalDump(d)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UnmarshalDump(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorkspaceID != "ws1" || got.Tables["workspaces"][0]["id"] != "ws1" {
			t.Errorf("round-trip = %+v", got)
		}
	})
	t.Run("unencodable value errors", func(t *testing.T) {
		d := &DBDump{Tables: map[string][]map[string]any{
			"t": {{"v": math.NaN()}},
		}}
		_, err := MarshalDump(d)
		if err == nil || !strings.Contains(err.Error(), "marshal dump") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unmarshal garbage errors", func(t *testing.T) {
		_, err := UnmarshalDump([]byte("{broken"))
		if err == nil || !strings.Contains(err.Error(), "unmarshal dump") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestQuoteIdentAndSortStrings(t *testing.T) {
	if got := quoteIdent(`na"me`); got != `"na""me"` {
		t.Errorf("quoteIdent = %s", got)
	}
	s := []string{"c", "a", "b"}
	sortStrings(s)
	if strings.Join(s, "") != "abc" {
		t.Errorf("sortStrings = %v", s)
	}
}
