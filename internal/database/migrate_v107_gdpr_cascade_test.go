package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV107_GDPRCascadeSchema verifies the schema changes the
// admin SAR cascade handlers depend on land correctly:
//
//   - memory_versions + inbox_items gain a nullable data_subject_id
//     column with a partial index (only populated rows). Legacy
//     inserts that omit the column must still succeed.
//   - gdpr_actions table accepts every documented (action, status)
//     value and rejects bogus values via the CHECK constraints.
//   - Subject lookup queries against the new indexes return the
//     correct rows.
//
// Mirrors the v105 peer_consent test shape — seed FK targets, hit
// every CHECK enum positively and at least one negative.
func TestMigrateV107_GDPRCascadeSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v107.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed FK targets.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','a@x'),('admin1','admin@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// memory_versions: legacy insert (no data_subject_id) still works.
	if _, err := db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref)
		VALUES ('mv1','ws1','/p/a','workspace','sha1',10,'/blob/sha1')`); err != nil {
		t.Fatalf("memory_versions legacy insert: %v", err)
	}
	// memory_versions: subject-scoped insert.
	if _, err := db.Exec(`INSERT INTO memory_versions
		(id, workspace_id, path, tier, sha256, bytes, payload_ref, data_subject_id)
		VALUES ('mv2','ws1','/p/b','peer','sha2',20,'/blob/sha2','u1')`); err != nil {
		t.Fatalf("memory_versions subject insert: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_versions
		WHERE workspace_id = 'ws1' AND data_subject_id = 'u1'`).Scan(&n); err != nil {
		t.Fatalf("memory_versions subject query: %v", err)
	}
	if n != 1 {
		t.Errorf("memory_versions subject count = %d, want 1", n)
	}

	// inbox_items: legacy insert (no data_subject_id) still works.
	if _, err := db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title)
		VALUES ('ib1','ws1','waitpoint','wp1','approve')`); err != nil {
		t.Fatalf("inbox_items legacy insert: %v", err)
	}
	// inbox_items: subject-scoped insert.
	if _, err := db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, data_subject_id)
		VALUES ('ib2','ws1','message','msg1','about user','u1')`); err != nil {
		t.Fatalf("inbox_items subject insert: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items
		WHERE workspace_id = 'ws1' AND data_subject_id = 'u1'`).Scan(&n); err != nil {
		t.Fatalf("inbox_items subject query: %v", err)
	}
	if n != 1 {
		t.Errorf("inbox_items subject count = %d, want 1", n)
	}

	// gdpr_actions: accepts every documented action value.
	for _, action := range []string{"export", "delete", "view"} {
		if _, err := db.Exec(`INSERT INTO gdpr_actions
			(id, workspace_id, data_subject_id, actor_user_id, action, status)
			VALUES (?, 'ws1', 'u1', 'admin1', ?, 'in_progress')`,
			"ga-"+action, action); err != nil {
			t.Errorf("gdpr_actions action=%q: %v", action, err)
		}
	}
	for _, status := range []string{"completed", "failed"} {
		if _, err := db.Exec(`INSERT INTO gdpr_actions
			(id, workspace_id, data_subject_id, actor_user_id, action, status)
			VALUES (?, 'ws1', 'u1', 'admin1', 'delete', ?)`,
			"ga-status-"+status, status); err != nil {
			t.Errorf("gdpr_actions status=%q: %v", status, err)
		}
	}

	// Negative: bogus action rejected.
	if _, err := db.Exec(`INSERT INTO gdpr_actions
		(id, workspace_id, data_subject_id, actor_user_id, action)
		VALUES ('ga-bogus','ws1','u1','admin1','exfiltrate')`); err == nil {
		t.Errorf("expected CHECK violation on action='exfiltrate'")
	}
	// Negative: bogus status rejected.
	if _, err := db.Exec(`INSERT INTO gdpr_actions
		(id, workspace_id, data_subject_id, actor_user_id, action, status)
		VALUES ('ga-bogus2','ws1','u1','admin1','delete','exploded')`); err == nil {
		t.Errorf("expected CHECK violation on status='exploded'")
	}

	// Subject lookup hits idx_gdpr_actions_subject.
	if err := db.QueryRow(`SELECT COUNT(*) FROM gdpr_actions
		WHERE workspace_id = 'ws1' AND data_subject_id = 'u1'`).Scan(&n); err != nil {
		t.Fatalf("gdpr_actions subject query: %v", err)
	}
	if n != 5 {
		t.Errorf("gdpr_actions subject count = %d, want 5", n)
	}
}
