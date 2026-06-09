package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV112_UserModelsSchema asserts the user_models index table
// lands with the right shape: keyed on (workspace_id, user_slug) with
// no agent_id (the model is per operator, not per agent+operator), and
// the UNIQUE constraint enforced.
func TestMigrateV112_UserModelsSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "v112.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := Migrate(context.Background(), db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed FK targets.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','W','w'),('ws2','W2','w2')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','a@x'),('u2','b@x')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	// Insert a model with NULL crew_id (allowed — crew_id is
	// informational, ON DELETE SET NULL).
	if _, err := db.Exec(`INSERT INTO user_models (id, workspace_id, user_id, user_slug, path, bytes)
		VALUES ('um1','ws1','u1','abc123','/users/abc123.md',200)`); err != nil {
		t.Fatalf("user_models insert: %v", err)
	}

	// UNIQUE(workspace_id, user_slug): same slug in same workspace for
	// a different user must collide.
	if _, err := db.Exec(`INSERT INTO user_models (id, workspace_id, user_id, user_slug, path, bytes)
		VALUES ('um2','ws1','u2','abc123','/users/abc123.md',300)`); err == nil {
		t.Errorf("expected UNIQUE violation on duplicate (workspace_id, user_slug)")
	}

	// Same slug in a DIFFERENT workspace is allowed — cross-workspace
	// isolation: the index does not collapse across tenants.
	if _, err := db.Exec(`INSERT INTO user_models (id, workspace_id, user_id, user_slug, path, bytes)
		VALUES ('um3','ws2','u1','abc123','/users/abc123.md',200)`); err != nil {
		t.Errorf("same slug in another workspace should be allowed: %v", err)
	}

	// Deleting the user cascades the model row away (GDPR alignment).
	if _, err := db.Exec(`DELETE FROM users WHERE id='u1'`); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE user_id='u1'`).Scan(&cnt); err != nil {
		t.Fatalf("count after cascade: %v", err)
	}
	if cnt != 0 {
		t.Errorf("expected user_models rows for u1 to cascade-delete; got %d", cnt)
	}
}
