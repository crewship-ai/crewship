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

	// Minimal schema kept intentionally small but production-shaped:
	// skills are globally namespaced (no workspace_id column, matching
	// the real schema as of v0.1.0-beta.1). Workspace scoping for
	// skills goes through agent_skills, which therefore has to exist
	// here too — the dump's new dependency-skip logic uses agent_skills
	// presence as the gate for the skills filter, and we want this test
	// suite to exercise that filter rather than rely on the gate.
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
			id TEXT PRIMARY KEY, name TEXT, body TEXT
		);
		CREATE TABLE agent_skills (
			id TEXT PRIMARY KEY,
			agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE,
			skill_id TEXT REFERENCES skills(id) ON DELETE CASCADE
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
		INSERT INTO skills (id, name, body) VALUES
			('s_1', 'git', 'git skill body'),
			('s_2', 'alien', 'do not leak');
		INSERT INTO agent_skills (id, agent_id, skill_id) VALUES
			('as_1', 'a_1', 's_1'),
			('as_2', 'a_3', 's_2');
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

// TestRestoreDump_PurgesSoftDeletedTombstones verifies that a row
// soft-deleted on the target (deleted_at set) does NOT block a
// restore that re-asserts the same primary key. Without the purge,
// INSERT OR IGNORE silently drops every bundle row whose ID
// collides with a tombstone and the runner reports "0 rows
// inserted" — an effective no-op restore.
//
// Mirrors the user-observed flow on dev3: DELETE /api/v1/crews/{id}
// soft-deletes, admin restores from a backup of that crew, the
// restore claimed success while landing nothing.
func TestRestoreDump_PurgesSoftDeletedTombstones(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE workspaces (
			id TEXT PRIMARY KEY, name TEXT, slug TEXT, deleted_at TEXT
		);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id),
			name TEXT, slug TEXT, deleted_at TEXT
		);
		-- Tombstone in target: same PK as bundle row, but soft-deleted.
		INSERT INTO workspaces (id, name, slug, deleted_at)
			VALUES ('ws_1', 'old name', 'ws-one', '2026-05-04T20:00:00Z');
		INSERT INTO crews (id, workspace_id, name, slug, deleted_at)
			VALUES ('c_1', 'ws_1', 'old crew', 'old-slug', '2026-05-04T20:00:00Z');
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"workspaces": {{"id": "ws_1", "name": "restored name", "slug": "ws-one"}},
			"crews":      {{"id": "c_1", "workspace_id": "ws_1", "name": "restored crew", "slug": "research"}},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(_ context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if stats.RowsInserted != 2 {
		t.Errorf("rows_inserted: got %d, want 2 (tombstones must purge before insert)", stats.RowsInserted)
	}

	var name, slug string
	if err := db.QueryRow(`SELECT name, slug FROM crews WHERE id='c_1'`).Scan(&name, &slug); err != nil {
		t.Fatalf("post-restore query: %v", err)
	}
	if name != "restored crew" || slug != "research" {
		t.Errorf("crew after restore: got name=%q slug=%q, want name=%q slug=%q", name, slug, "restored crew", "research")
	}

	// And no orphan tombstone left behind.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE id='c_1' AND deleted_at IS NOT NULL AND deleted_at != ''`).Scan(&count); err != nil {
		t.Fatalf("tombstone count: %v", err)
	}
	if count != 0 {
		t.Errorf("tombstone survived purge: %d rows still soft-deleted", count)
	}
}

// TestRestoreDump_LiveRowsUntouched verifies that rows WITHOUT a
// tombstone (deleted_at NULL) are NOT purged. Restore must not
// destroy live data, only resurrect rows the bundle wants to claim.
func TestRestoreDump_LiveRowsUntouched(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`
		CREATE TABLE crews (
			id TEXT PRIMARY KEY, workspace_id TEXT, name TEXT, slug TEXT, deleted_at TEXT
		);
		-- Live row in target. Bundle wants to insert with same PK.
		INSERT INTO crews (id, workspace_id, name, slug, deleted_at)
			VALUES ('c_1', 'ws_1', 'live crew', 'live-slug', NULL);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	dump := &DBDump{
		WorkspaceID: "ws_1",
		Tables: map[string][]map[string]any{
			"crews": {{"id": "c_1", "workspace_id": "ws_1", "name": "bundle wins?", "slug": "bundle-slug"}},
		},
	}

	stats, err := RestoreDumpTx(context.Background(), db, dump, func(_ context.Context) error { return nil })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// INSERT OR IGNORE skips the live row; the no-op restore guard at
	// runner_restore.go is the safety net there. The point of THIS test
	// is to confirm we did NOT delete the live row.
	if stats.RowsInserted != 0 {
		t.Errorf("rows_inserted: got %d, want 0 (live PK must be preserved)", stats.RowsInserted)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM crews WHERE id='c_1'`).Scan(&name); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "live crew" {
		t.Errorf("live row mutated: name=%q want 'live crew'", name)
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
