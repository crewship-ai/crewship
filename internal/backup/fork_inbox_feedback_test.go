package backup_test

// #2274 regression: inbox_items (UNIQUE(kind, source_id) → per
// workspace) and message_feedback (UNIQUE(message_id, user_id, signal)
// → per workspace) must survive a forked restore. Before the keys were
// scoped, the fork's rows collided with the source's on the same
// instance and INSERT OR IGNORE dropped every one — inbox_items took
// its inbox_item_reads children with it. There is no token to re-mint
// in these tables; the fix is the scoped key, and this test also pins
// the upsert conflict targets against the migrated schema, because
// shipping the index without the writers (or vice versa) breaks at the
// first ON CONFLICT.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func seedInboxAndFeedback(t *testing.T, db *sql.DB, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state)
		 VALUES ('ibx_src_1', ?, 'waitpoint', 'wp_token_9', 'Approve the deploy', 'unread')`,
		workspaceID); err != nil {
		t.Fatalf("seed inbox item: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
		 VALUES ('ibx_src_1', 'u_admin', '2026-09-24T12:00:00Z')`); err != nil {
		t.Fatalf("seed inbox read: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO message_feedback (id, workspace_id, message_id, signal, user_id)
		 VALUES ('fb_src_1', ?, 'msg_1', 'helpful', 'u_admin')`,
		workspaceID); err != nil {
		t.Fatalf("seed message feedback: %v", err)
	}
}

func forkBackup(t *testing.T, db *sql.DB, path, passphrase, slug string) *backup.RestoreResult {
	t.Helper()
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	res, err := backup.RestoreBackup(context.Background(), db, backup.RestoreOptions{
		Path:        path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: slug,
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace %s: %v", slug, err)
	}
	return res
}

func countInboxRows(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func TestForkedRestore_InboxAndFeedback(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedInboxAndFeedback(t, source, workspaceID)

	const passphrase = "fork-inbox-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	fork1 := forkBackup(t, source, created.Path, passphrase, "e2e-ws-inbox-fork-1").RestoredWorkspaceID
	assertNoFKViolations(t, source, "after inbox/feedback fork 1")

	// inbox_items: the fork's row lands beside the source's, same
	// (kind, source_id), different workspace.
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND kind = 'waitpoint' AND source_id = 'wp_token_9'`,
		fork1); got != 1 {
		t.Errorf("fork 1 inbox_items: got %d row(s), want 1 — the #2274 drop is back", got)
	}
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_items WHERE kind = 'waitpoint' AND source_id = 'wp_token_9'`); got != 2 {
		t.Errorf("instance-wide (kind='waitpoint', source_id='wp_token_9') rows = %d, want 2 (source + fork)", got)
	}
	// inbox_item_reads followed its parent — the remapped item id, not
	// the source's.
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_item_reads r JOIN inbox_items i ON i.id = r.inbox_item_id
		 WHERE i.workspace_id = ?`, fork1); got != 1 {
		t.Errorf("fork 1 inbox_item_reads: got %d row(s), want 1 following the remapped parent", got)
	}
	// message_feedback lands under the fork's workspace_id.
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM message_feedback WHERE workspace_id = ? AND message_id = 'msg_1' AND user_id = 'u_admin' AND signal = 'helpful'`,
		fork1); got != 1 {
		t.Errorf("fork 1 message_feedback: got %d row(s), want 1 — the #2274 drop is back", got)
	}
	// The source rows are untouched.
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ?`, workspaceID); got != 1 {
		t.Errorf("source inbox_items count = %d, want 1", got)
	}
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM message_feedback WHERE workspace_id = ?`, workspaceID); got != 1 {
		t.Errorf("source message_feedback count = %d, want 1", got)
	}

	// A SECOND fork of the same bundle lands its own copies: the scoped
	// key is a namespace per workspace, not a one-shot dodge.
	fork2 := forkBackup(t, source, created.Path, passphrase, "e2e-ws-inbox-fork-2").RestoredWorkspaceID
	assertNoFKViolations(t, source, "after inbox/feedback fork 2")
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_items WHERE kind = 'waitpoint' AND source_id = 'wp_token_9'`); got != 3 {
		t.Errorf("after fork 2: %d row(s) share (kind, source_id), want 3 (source + two forks)", got)
	}
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM message_feedback WHERE message_id = 'msg_1' AND user_id = 'u_admin' AND signal = 'helpful'`); got != 3 {
		t.Errorf("after fork 2: %d feedback row(s) share the tuple, want 3", got)
	}
	_ = fork2

	// The upsert paths keep working against the migrated schema: the
	// conflict targets must match the scoped indexes exactly, or every
	// inbox write fails with "ON CONFLICT clause does not match any
	// PRIMARY KEY or UNIQUE constraint". These are the statement shapes
	// internal/inbox (writer.go) and internal/api (message_feedback.go)
	// run in production.
	if _, err := source.ExecContext(ctx, `
		INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, state)
		VALUES ('ibx_up_1', ?, 'waitpoint', 'wp_token_9', 'Approve the deploy (updated)', 'unread')
		ON CONFLICT(workspace_id, kind, source_id) DO UPDATE SET title = excluded.title`,
		fork1); err != nil {
		t.Errorf("inbox upsert against scoped index: %v", err)
	}
	if got := queryStringValue(t, source,
		`SELECT title FROM inbox_items WHERE workspace_id = ? AND kind = 'waitpoint' AND source_id = 'wp_token_9'`, fork1); got != "Approve the deploy (updated)" {
		t.Errorf("inbox upsert did not update the fork's row: title = %q", got)
	}
	if _, err := source.ExecContext(ctx, `
		INSERT INTO message_feedback (id, workspace_id, message_id, signal, user_id)
		VALUES ('fb_up_1', ?, 'msg_1', 'helpful', 'u_admin')
		ON CONFLICT(workspace_id, message_id, user_id, signal) DO UPDATE SET reason = excluded.reason`,
		fork1); err != nil {
		t.Errorf("feedback upsert against scoped index: %v", err)
	}
	// And the upsert on one workspace must not have touched the other's row.
	if got := countInboxRows(t, source,
		`SELECT COUNT(*) FROM inbox_items WHERE title = 'Approve the deploy (updated)'`); got != 1 {
		t.Errorf("upsert leaked across workspaces: %d rows updated, want 1", got)
	}
}
