package database

import (
	"context"
	"database/sql"
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
//
// Migrates up to THIS version only (not head) before seeding + replaying,
// via applyRegistry directly: 20260905083300_inbox_attention_contract.sql
// (B10, #2364) rebuilds inbox_items a second time, and once that has run
// the table sits at its post-image permanently — a ledger-row delete never
// reverts DDL, only re-executes it — so replaying this migration's own
// bare `SELECT * FROM inbox_items` against an already-further-widened
// table would feed its 23-column inbox_items_new too many values. The
// final block continues to head to prove the two rebuilds still compose.
func TestMigrate_InboxKindRunNeedsHuman_PreservesInboxItemReads(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "inbox_kind_rnh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Migrate up to and including THIS migration only — not to head — so
	// the table is genuinely at its post-image (23 columns) when we seed
	// data and replay below. Migrating straight to head first (as this test
	// did before #2364/B10) would let 20260905083300_inbox_attention_
	// contract.sql's later rebuild widen inbox_items to 26 columns before
	// the replay ever runs, which is a different, already-covered scenario
	// (see the continue-to-head assertions at the end of this test) — not
	// the isolated-replay one this test exists to pin.
	if err := ensureMigrationsTable(ctx, db.DB); err != nil {
		t.Fatalf("ensure migrations table: %v", err)
	}
	var upToThisMigration []migration
	for _, m := range migrations {
		if m.version <= 20260904233805 {
			upToThisMigration = append(upToThisMigration, m)
		}
	}
	if err := applyRegistry(ctx, db.DB, upToThisMigration, silent); err != nil {
		t.Fatalf("migrate up to this migration: %v", err)
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

	// Replay THIS migration specifically, in true isolation: clear its
	// ledger row and re-apply only the migrations at-or-below its own
	// version (see the type doc comment for why not the full Migrate()).
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260904233805`); err != nil {
		t.Fatalf("clear migration marker: %v", err)
	}
	if err := applyRegistry(ctx, db.DB, upToThisMigration, silent); err != nil {
		t.Fatalf("re-apply up to and including this migration: %v", err)
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

	// Continue forward to head — this proves the chain still composes: a
	// migration replayed in isolation, then followed by every migration
	// after it (including 20260905083300's own inbox_items rebuild), must
	// leave the SAME read marker and inbox item intact through a SECOND
	// rebuild.
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("continue to head: %v", err)
	}
	if err := db.QueryRow(`SELECT read_at, user_id FROM inbox_item_reads WHERE inbox_item_id = 'ib_rnh'`).
		Scan(&readAt, &userID); err != nil {
		t.Fatalf("read marker did not survive the second rebuild (B10's migration): %v", err)
	}
	if userID != "user_rnh" || readAt != "2026-09-01T00:00:00.000Z" {
		t.Errorf("read marker changed after the second rebuild: user_id=%q read_at=%q", userID, readAt)
	}
	var threadKey sql.NullString
	if err := db.QueryRow(`SELECT thread_key FROM inbox_items WHERE id = 'ib_rnh'`).Scan(&threadKey); err != nil {
		t.Fatalf("B10's thread_key column missing after the chained rebuild: %v", err)
	}
	if threadKey.Valid {
		t.Errorf("thread_key should be NULL for a pre-B10 row that never set one, got %q", threadKey.String)
	}
}
