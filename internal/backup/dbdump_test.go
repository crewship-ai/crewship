package backup

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newDumpTestDB builds a minimal DB with the subset of Crewship tables
// that DumpWorkspace actually reads. Keeping it local to the test avoids
// pulling the full migrate.go tangle into the backup package tests.
func newDumpTestDB(t *testing.T) *sql.DB {
	t.Helper()
	// Use a fresh on-disk DB per test so connection-pool behaviour
	// does not split the schema across isolated ":memory:" databases
	// (which happens whenever database/sql opens a second connection
	// — e.g. DumpCrew's pre-transaction QueryRow vs. its BeginTx).
	// t.TempDir auto-cleans the file so there is no leftover state.
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(`
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY, name TEXT, slug TEXT, created_at TEXT
		);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id),
			name TEXT, slug TEXT, created_at TEXT
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY, crew_id TEXT REFERENCES crews(id),
			name TEXT, created_at TEXT
		);
		CREATE TABLE skills (
			id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id),
			name TEXT, body TEXT
		);

		INSERT INTO workspaces (id, name, slug) VALUES
			('ws_1', 'Workspace One', 'ws-one'),
			('ws_2', 'Workspace Two', 'ws-two');
		INSERT INTO crews (id, workspace_id, name, slug) VALUES
			('c_1_a', 'ws_1', 'A', 'a'),
			('c_1_b', 'ws_1', 'B', 'b'),
			('c_2_a', 'ws_2', 'Other', 'other');
		INSERT INTO agents (id, crew_id, name) VALUES
			('a_1', 'c_1_a', 'Alice'),
			('a_2', 'c_1_b', 'Bob'),
			('a_3', 'c_2_a', 'Ghost');
		INSERT INTO skills (id, workspace_id, name, body) VALUES
			('s_1', 'ws_1', 'git', 'git skill body'),
			('s_2', 'ws_2', 'alien', 'do not leak');
	`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestDumpWorkspace_Isolation(t *testing.T) {
	db := newDumpTestDB(t)
	dump, err := DumpWorkspace(context.Background(), db, "ws_1")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if got := len(dump.Tables["workspaces"]); got != 1 {
		t.Errorf("workspaces: got %d rows, want 1", got)
	}
	if got := len(dump.Tables["crews"]); got != 2 {
		t.Errorf("crews: got %d rows, want 2", got)
	}
	if got := len(dump.Tables["agents"]); got != 2 {
		t.Errorf("agents: got %d rows, want 2 (isolation)", got)
	}
	for _, row := range dump.Tables["agents"] {
		if row["id"] == "a_3" {
			t.Error("agent from another workspace leaked into dump (a_3)")
		}
	}
	if got := len(dump.Tables["skills"]); got != 1 {
		t.Errorf("skills: got %d rows, want 1 (isolation)", got)
	}
	for _, row := range dump.Tables["skills"] {
		if row["id"] == "s_2" {
			t.Error("skill from another workspace leaked into dump (s_2)")
		}
	}
}

func TestDumpCrew_NarrowsToSingleCrew(t *testing.T) {
	db := newDumpTestDB(t)
	dump, err := DumpCrew(context.Background(), db, "c_1_a")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if got := len(dump.Tables["crews"]); got != 1 {
		t.Errorf("crews: got %d rows, want 1", got)
	}
	if got := len(dump.Tables["agents"]); got != 1 {
		t.Errorf("agents: got %d rows, want 1", got)
	}
	if agents := dump.Tables["agents"]; len(agents) == 1 && agents[0]["id"] != "a_1" {
		t.Errorf("wrong agent dumped: %v", agents[0]["id"])
	}
}

func TestDumpWorkspace_UnknownTablesSkipped(t *testing.T) {
	// Even though BackupTables includes tables like memory_backups that
	// don't exist in this minimal schema, DumpWorkspace must not error.
	db := newDumpTestDB(t)
	_, err := DumpWorkspace(context.Background(), db, "ws_1")
	if err != nil {
		t.Errorf("dump should skip missing tables silently, got %v", err)
	}
}

func TestRestoreDump_RoundTrip(t *testing.T) {
	// Dump from one DB, restore into a fresh one with the same schema,
	// then re-dump and check row counts.
	src := newDumpTestDB(t)
	dump, err := DumpWorkspace(context.Background(), src, "ws_1")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}

	dst := newDumpTestDB(t)
	// Wipe the destination so we're truly restoring.
	_, _ = dst.Exec(`DELETE FROM agents; DELETE FROM skills; DELETE FROM crews; DELETE FROM workspaces`)

	if err := RestoreDump(context.Background(), dst, dump); err != nil {
		t.Fatalf("restore: %v", err)
	}

	dumpBack, err := DumpWorkspace(context.Background(), dst, "ws_1")
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if got := len(dumpBack.Tables["crews"]); got != 2 {
		t.Errorf("round-trip crews: got %d, want 2", got)
	}
	if got := len(dumpBack.Tables["agents"]); got != 2 {
		t.Errorf("round-trip agents: got %d, want 2", got)
	}
}

func TestMarshalUnmarshalDump(t *testing.T) {
	d := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {
				{"id": "ws_1", "name": "One"},
			},
		},
	}
	raw, err := MarshalDump(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty marshal output")
	}
	d2, err := UnmarshalDump(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d2.WorkspaceID != "ws_1" {
		t.Errorf("roundtrip workspace id mismatch: %v", d2.WorkspaceID)
	}
	if len(d2.Tables["workspaces"]) != 1 {
		t.Errorf("roundtrip row count mismatch: %v", d2.Tables["workspaces"])
	}
}
