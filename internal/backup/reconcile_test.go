package backup

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// newReconcileTestDB builds the minimum schema the reconciler walks:
// users (with UNIQUE email), and one table that FKs into users.id.
func newReconcileTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT UNIQUE);
		CREATE TABLE workspaces (id TEXT PRIMARY KEY, slug TEXT);
		CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id));
		CREATE TABLE crew_members (
			id TEXT PRIMARY KEY,
			crew_id TEXT REFERENCES crews(id),
			user_id TEXT REFERENCES users(id)
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			workspace_id TEXT REFERENCES workspaces(id),
			created_by TEXT REFERENCES users(id)
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestReconcileUsersByEmail_RewritesBundleUserIDToTargetMatch(t *testing.T) {
	ctx := context.Background()
	db := newReconcileTestDB(t)
	// Target already has admin under email X with id `u_target`.
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u_target', 'admin@x.test')`); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"users": {
				{"id": "u_bundle", "email": "admin@x.test"},
			},
			"crew_members": {
				{"id": "cm_1", "crew_id": "c_1", "user_id": "u_bundle"},
			},
			"chats": {
				{"id": "ch_1", "workspace_id": "ws_1", "created_by": "u_bundle"},
			},
		},
	}
	remap, err := ReconcileUsersByEmail(ctx, tx, dump)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got, want := remap["u_bundle"], "u_target"; got != want {
		t.Errorf("remap u_bundle → got %q, want %q", got, want)
	}
	// dump's user row was rewritten in-place so INSERT OR IGNORE no-ops.
	if got := dump.Tables["users"][0]["id"]; got != "u_target" {
		t.Errorf("bundle user id not rewritten: got %v", got)
	}
	// crew_members.user_id FK rewritten.
	if got := dump.Tables["crew_members"][0]["user_id"]; got != "u_target" {
		t.Errorf("crew_members.user_id not rewritten: got %v", got)
	}
	// chats.created_by FK rewritten.
	if got := dump.Tables["chats"][0]["created_by"]; got != "u_target" {
		t.Errorf("chats.created_by not rewritten: got %v", got)
	}
}

func TestReconcileUsersByEmail_NoTargetMatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	db := newReconcileTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"users": {
				{"id": "u_bundle", "email": "fresh@x.test"},
			},
			"crew_members": {
				{"id": "cm_1", "user_id": "u_bundle"},
			},
		},
	}
	remap, err := ReconcileUsersByEmail(ctx, tx, dump)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(remap) != 0 {
		t.Errorf("expected empty remap, got %v", remap)
	}
	// Bundle row untouched — INSERT will land with original id.
	if got := dump.Tables["users"][0]["id"]; got != "u_bundle" {
		t.Errorf("bundle user id should not be rewritten when no match; got %v", got)
	}
	if got := dump.Tables["crew_members"][0]["user_id"]; got != "u_bundle" {
		t.Errorf("FK should not be rewritten when no match; got %v", got)
	}
}

func TestReconcileUsersByEmail_SameIDSameEmailIsNoOp(t *testing.T) {
	ctx := context.Background()
	db := newReconcileTestDB(t)
	// Target has SAME id and SAME email as bundle — restoring into
	// source instance, no work to do.
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u_same', 'admin@x.test')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	dump := &DBDump{
		Tables: map[string][]map[string]any{
			"users": {{"id": "u_same", "email": "admin@x.test"}},
		},
	}
	remap, err := ReconcileUsersByEmail(ctx, tx, dump)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(remap) != 0 {
		t.Errorf("aligned bundle/target should produce empty remap, got %v", remap)
	}
}

func TestReconcileUsersByEmail_HandlesEmptyDump(t *testing.T) {
	ctx := context.Background()
	db := newReconcileTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback()

	remap, err := ReconcileUsersByEmail(ctx, tx, &DBDump{Tables: map[string][]map[string]any{}})
	if err != nil {
		t.Fatalf("empty dump should be no-op, got %v", err)
	}
	if remap != nil && len(remap) != 0 {
		t.Errorf("empty dump should return nil/empty remap, got %v", remap)
	}
	// nil dump also OK.
	if _, err := ReconcileUsersByEmail(ctx, tx, nil); err != nil {
		t.Errorf("nil dump should be no-op, got %v", err)
	}
}
