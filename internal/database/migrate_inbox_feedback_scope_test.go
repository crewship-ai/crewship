package database

// #2274's two scoping migrations (inbox_items' dedupe index,
// message_feedback's rebuilt unique key) have to upgrade a POPULATED
// database — every install in the field has rows in both tables, and
// the rebuild path (create/copy/drop/rename) is exactly the kind of SQL
// that passes on an empty table and eats data on a full one. This test
// lands the schema as it shipped BEFORE the scoping, seeds rows, then
// applies the remaining migrations and asserts the rows survived, the
// old index is gone, the new shapes are in place, and the production
// upsert conflict targets resolve against them.

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrate_InboxFeedbackScope_UpgradeWithExistingRows(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "pre-scoping.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// The schema as it shipped immediately before the scoping
	// migrations (20260922182944 is the last older stamp in the
	// registry; 20260924141200/1 are the two under test).
	if err := applyMigrationsUpTo(ctx, db.DB, 20260922182944, logger); err != nil {
		t.Fatalf("apply pre-scoping migrations: %v", err)
	}

	// Seed rows legal under the OLD (instance-wide) keys — that is all
	// an upgrading database can hold, by definition of the old key.
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES
		('ws_a', 'WS A', 'ws-a'), ('ws_b', 'WS B', 'ws-b')`); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'u1@e2e.test', 'One')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ibx_a', 'ws_a', 'waitpoint', 'wp_keep', 'kept'),
		       ('ibx_b', 'ws_b', 'waitpoint', 'wp_other', 'kept too')`); err != nil {
		t.Fatalf("seed inbox items: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
		VALUES ('ibx_a', 'u1', '2026-09-01T10:00:00Z')`); err != nil {
		t.Fatalf("seed inbox read: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO message_feedback (id, workspace_id, message_id, signal, user_id)
		VALUES ('fb_a', 'ws_a', 'msg_keep', 'helpful', 'u1'),
		       ('fb_b', 'ws_b', 'msg_other', 'not_helpful', 'u1')`); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	// Apply the rest — the two scoping migrations included.
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("Migrate through the scoping migrations: %v", err)
	}

	// Every seeded row survived the rebuild.
	for _, check := range []struct{ q, want string }{
		{`SELECT COUNT(*) FROM inbox_items`, "2"},
		{`SELECT COUNT(*) FROM inbox_item_reads`, "1"},
		{`SELECT COUNT(*) FROM message_feedback`, "2"},
		{`SELECT title FROM inbox_items WHERE id = 'ibx_a'`, "kept"},
		{`SELECT signal FROM message_feedback WHERE id = 'fb_a'`, "helpful"},
	} {
		var got string
		if err := db.QueryRow(check.q).Scan(&got); err != nil {
			t.Fatalf("query %q: %v", check.q, err)
		}
		if got != check.want {
			t.Errorf("%q = %q, want %q", check.q, got, check.want)
		}
	}

	// The old unique index is gone; the workspace-scoped one is in place.
	// (Named precisely — inbox_items carries several NON-unique indexes a
	// broad LIKE would drag in.)
	var oldIdx, newIdx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = 'idx_inbox_items_kind_source'`).Scan(&oldIdx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = 'idx_inbox_items_workspace_kind_source'`).Scan(&newIdx); err != nil {
		t.Fatal(err)
	}
	if oldIdx != 0 || newIdx != 1 {
		t.Errorf("inbox dedupe indexes: old present=%d new present=%d, want 0 and 1", oldIdx, newIdx)
	}
	var fbSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'message_feedback'`).
		Scan(&fbSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fbSQL, "UNIQUE(workspace_id, message_id, user_id, signal)") {
		t.Errorf("message_feedback rebuilt without the workspace-scoped key:\n%s", fbSQL)
	}
	// The rebuild died with the old table, so all four of its indexes
	// have to exist on the rebuilt one — a missed one is a silent full
	// table scan on a hot path, not an error.
	for _, idx := range []string{"idx_feedback_trace", "idx_feedback_ws_created", "idx_feedback_message", "idx_message_feedback_chat"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("index %s present %d time(s) after the rebuild, want 1", idx, n)
		}
	}

	// The production upsert shapes resolve against the upgraded schema,
	// and the widened keys now admit the fork-twin rows they exist for:
	// the same (kind, source_id) in a second workspace, and the same
	// feedback tuple in a second workspace — both impossible before.
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ibx_a2', 'ws_b', 'waitpoint', 'wp_keep', 'fork twin')
		ON CONFLICT(workspace_id, kind, source_id) DO UPDATE SET title = excluded.title`); err != nil {
		t.Fatalf("scoped inbox upsert on upgraded schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO message_feedback (id, workspace_id, message_id, signal, user_id)
		VALUES ('fb_a2', 'ws_b', 'msg_keep', 'helpful', 'u1')
		ON CONFLICT(workspace_id, message_id, user_id, signal) DO UPDATE SET reason = excluded.reason`); err != nil {
		t.Fatalf("scoped feedback upsert on upgraded schema: %v", err)
	}
	var twinInbox, twinFB int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='waitpoint' AND source_id='wp_keep'`).Scan(&twinInbox); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE message_id='msg_keep' AND user_id='u1' AND signal='helpful'`).Scan(&twinFB); err != nil {
		t.Fatal(err)
	}
	if twinInbox != 2 || twinFB != 2 {
		t.Errorf("fork twins after upgrade: inbox=%d feedback=%d, want 2 and 2", twinInbox, twinFB)
	}
}
