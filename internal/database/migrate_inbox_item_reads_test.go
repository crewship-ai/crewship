package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// inbox_item_reads (A7, PRD-ISSUES-AND-ROUTINES-2026.md §9.7) — the
// per-user read marker that replaces inbox_items.read_at/read_by_user_id
// as the source of "did THIS caller read THIS item". These tests pin the
// schema shape (composite PK, ON DELETE behaviour) and the backfill that
// must convert every pre-existing (read_at, read_by_user_id) pair into
// exactly one row.

func seedInboxItemReadsFixture(t *testing.T, db *DB) (wsID, userA, userB, itemID string) {
	t.Helper()
	wsID, userA, userB, itemID = "ws_iir", "user_iir_a", "user_iir_b", "ib_iir"
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS', 'ws-iir')`, wsID)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES (?, 'iir-a@example.com')`, userA)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES (?, 'iir-b@example.com')`, userB)
	execMigrationFixture(t, db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state)
		VALUES (?, ?, 'message', 'src-iir', 'test item', 'unread')`, itemID, wsID)
	return
}

// The whole point of A7: two users can each carry their own read marker on
// the SAME inbox item, and one PRIMARY KEY per (item, user) pair enforces
// that a "mark read" is an idempotent upsert rather than a duplicate row.
func TestMigrate_InboxItemReads_CompositePK(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	_, userA, userB, itemID := seedInboxItemReadsFixture(t, db)

	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userA); err != nil {
		t.Fatalf("insert read marker for user A: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userB); err != nil {
		t.Fatalf("insert read marker for user B (independent of A): %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ?`, itemID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 independent read rows (one per user) on the same item, got %d", n)
	}

	// A second read marker for the SAME (item, user) pair must collide on
	// the PK, not create a second row — this is what makes PATCH read an
	// idempotent upsert rather than an accumulating log.
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userA); err == nil {
		t.Fatal("a duplicate (inbox_item_id, user_id) insert was accepted; PRIMARY KEY (inbox_item_id, user_id) should reject it")
	} else if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") && !strings.Contains(strings.ToUpper(err.Error()), "PRIMARY KEY") {
		t.Errorf("duplicate insert failed for the wrong reason: %v", err)
	}
}

// Deleting the inbox item must take its read markers with it — an orphan
// read row for an item that no longer exists cannot answer any question a
// caller would ask it.
func TestMigrate_InboxItemReads_CascadesOnItemDelete(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	_, userA, _, itemID := seedInboxItemReadsFixture(t, db)

	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userA); err != nil {
		t.Fatalf("insert read marker: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM inbox_items WHERE id = ?`, itemID); err != nil {
		t.Fatalf("delete inbox item: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ?`, itemID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("read marker survived its inbox item's deletion; ON DELETE CASCADE on inbox_item_id should have removed it")
	}
}

// Deleting the user must take their own read markers with it, without
// touching any other user's marker on the same item.
func TestMigrate_InboxItemReads_CascadesOnUserDelete(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	_, userA, userB, itemID := seedInboxItemReadsFixture(t, db)

	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userA); err != nil {
		t.Fatalf("insert read marker A: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, ?)`, itemID, userB); err != nil {
		t.Fatalf("insert read marker B: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userA); err != nil {
		t.Fatalf("delete user A: %v", err)
	}

	var aCount, bCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ? AND user_id = ?`, itemID, userA).Scan(&aCount); err != nil {
		t.Fatalf("count A: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ? AND user_id = ?`, itemID, userB).Scan(&bCount); err != nil {
		t.Fatalf("count B: %v", err)
	}
	if aCount != 0 {
		t.Errorf("user A's read marker survived their own deletion, want cascaded delete")
	}
	if bCount != 1 {
		t.Errorf("user B's read marker was removed by user A's deletion, want it untouched (got count=%d)", bCount)
	}
}

// TestMigrate_InboxItemReads_Backfill re-runs the migration against a
// database that already carries pre-A7 rows — read_at/read_by_user_id
// populated the way every workspace on `main` has them today — and
// asserts each becomes exactly one inbox_item_reads row. Mirrors
// migrate_backfill_onboarding_skipped_at_test.go's re-migrate shape:
// clear this migration's _migrations ledger entry so Migrate replays it
// against data seeded as if it pre-dated the migration.
func TestMigrate_InboxItemReads_Backfill(t *testing.T) {
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "iir_backfill.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_bf', 'WS', 'ws-bf')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('user_bf', 'bf@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Legacy-shaped row: read_at/read_by_user_id populated, exactly as a
	// real pre-A7 "someone read this" row looks. NOT NULL is required by
	// the backfill's WHERE clause, so this row is the positive case.
	if _, err := db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, state, read_at, read_by_user_id)
		VALUES ('ib_bf_read', 'ws_bf', 'message', 'src-bf-1', 'read item', 'read',
		        '2026-08-01T00:00:00.000Z', 'user_bf')`); err != nil {
		t.Fatalf("seed read inbox item: %v", err)
	}
	// Negative case: an item nobody has read yet must NOT produce a row.
	if _, err := db.Exec(`INSERT INTO inbox_items
		(id, workspace_id, kind, source_id, title, state)
		VALUES ('ib_bf_unread', 'ws_bf', 'message', 'src-bf-2', 'unread item', 'unread')`); err != nil {
		t.Fatalf("seed unread inbox item: %v", err)
	}

	// Replay the migration: drop its ledger row and re-run the full chain.
	// (loadFileMigrations parses the filename's leading digits as the
	// version, so this must match the migration file's stamp exactly.)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = 20260901180610`); err != nil {
		t.Fatalf("clear inbox_item_reads migration marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate (backfill): %v", err)
	}

	var readAt, userID string
	if err := db.QueryRow(`SELECT read_at, user_id FROM inbox_item_reads WHERE inbox_item_id = 'ib_bf_read'`).
		Scan(&readAt, &userID); err != nil {
		t.Fatalf("backfilled row missing for ib_bf_read: %v", err)
	}
	if userID != "user_bf" {
		t.Errorf("backfilled user_id = %q, want user_bf", userID)
	}
	if readAt != "2026-08-01T00:00:00.000Z" {
		t.Errorf("backfilled read_at = %q, want the original read_at preserved verbatim", readAt)
	}

	var unreadCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = 'ib_bf_unread'`).Scan(&unreadCount); err != nil {
		t.Fatalf("count for unread item: %v", err)
	}
	if unreadCount != 0 {
		t.Errorf("an item nobody read produced %d backfilled rows, want 0", unreadCount)
	}

	// Re-running Migrate a THIRD time (marker not cleared this time) must
	// not error and must not duplicate the row — INSERT OR IGNORE plus the
	// migrations ledger both guard idempotency.
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("idempotent re-Migrate: %v", err)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = 'ib_bf_read'`).Scan(&total); err != nil {
		t.Fatalf("count after idempotent re-migrate: %v", err)
	}
	if total != 1 {
		t.Errorf("backfilled row count after a repeat Migrate = %d, want 1 (no duplication)", total)
	}
}
