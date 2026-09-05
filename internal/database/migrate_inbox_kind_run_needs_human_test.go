package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrate_InboxKindRunNeedsHuman_PreservesInboxItemReads pins the
// data-safety fix on 20260904233805_inbox_kind_run_needs_human.sql (work
// package B6, #2349, caught in code review): that migration widens
// inbox_items.kind via the CREATE-copy-DROP-RENAME rebuild every prior
// CHECK-widening migration on this table used (v90, v162,
// 20260728110000, 20260901180845) — but unlike all of those, this is the
// FIRST one to run after inbox_item_reads existed
// (20260902071500_inbox_item_reads.sql), whose `inbox_item_id REFERENCES
// inbox_items(id) ON DELETE CASCADE` fires against the DROPPED inbox_items
// table with foreign_keys=ON, silently deleting every read marker unless
// the migration explicitly preserves and restores them.
//
// Replays the migration (mirrors migrate_inbox_item_reads_test.go's own
// backfill-replay shape) against a database that already has a real
// inbox_item_reads row, and asserts it survives.
func TestMigrate_InboxKindRunNeedsHuman_PreservesInboxItemReads(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "inbox_kind_rnh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_rnh', 'WS', 'ws-rnh')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_rnh', 'rnh@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state)
		VALUES ('ib_rnh', 'ws_rnh', 'message', 'src-rnh', 'test item', 'read')`); err != nil {
		t.Fatalf("seed inbox item: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
		VALUES ('ib_rnh', 'user_rnh', '2026-09-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed read marker: %v", err)
	}

	// Replay this migration specifically: clear its ledger row and re-run
	// the full chain. (loadFileMigrations parses the filename's leading
	// digits as the version, so this must match the file's stamp exactly.)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260904233805`); err != nil {
		t.Fatalf("clear migration marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}

	var readAt, userID string
	if err := db.QueryRow(`SELECT read_at, user_id FROM inbox_item_reads WHERE inbox_item_id = 'ib_rnh'`).
		Scan(&readAt, &userID); err != nil {
		t.Fatalf("read marker did not survive the migration replay: %v", err)
	}
	if userID != "user_rnh" {
		t.Errorf("user_id = %q, want user_rnh", userID)
	}
	if readAt != "2026-09-01T00:00:00.000Z" {
		t.Errorf("read_at = %q, want the original value preserved verbatim", readAt)
	}

	// The inbox item itself, and the widened CHECK, must also have
	// survived the rebuild intact.
	var kind string
	if err := db.QueryRow(`SELECT kind FROM inbox_items WHERE id = 'ib_rnh'`).Scan(&kind); err != nil {
		t.Fatalf("inbox item did not survive the migration replay: %v", err)
	}
	if kind != "message" {
		t.Errorf("kind = %q, want message (unchanged)", kind)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state)
		VALUES ('ib_rnh_2', 'ws_rnh', 'run_needs_human', 'src-rnh-2', 'needs human', 'unread')`); err != nil {
		t.Errorf("run_needs_human rejected by the widened CHECK: %v", err)
	}
}
