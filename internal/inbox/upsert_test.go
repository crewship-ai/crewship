package inbox

import (
	"context"
	"testing"
)

// UpsertMessage is the dedupe primitive behind the "your agent replied"
// notification: one row per (kind, source_id); a second reply refreshes
// title/body/timestamps and resurrects the row as unread instead of
// piling up siblings.

func TestUpsertMessage_InsertsNewRow(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	if err := UpsertMessage(ctx, db, quietLogger(), Item{
		WorkspaceID:  "ws1",
		Kind:         KindMessage,
		SourceID:     "chat_reply_c1_u1",
		TargetUserID: "u1",
		Title:        "Atlas replied",
		BodyMD:       "first reply",
		SenderType:   "agent",
		SenderID:     "a1",
		SenderName:   "Atlas",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var state, title string
	if err := db.QueryRow(`SELECT state, title FROM inbox_items WHERE kind='message' AND source_id='chat_reply_c1_u1'`).
		Scan(&state, &title); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if state != "unread" || title != "Atlas replied" {
		t.Errorf("state=%q title=%q, want unread / Atlas replied", state, title)
	}
}

func TestUpsertMessage_SecondCallRefreshesInsteadOfDuplicating(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	item := Item{
		WorkspaceID:  "ws1",
		Kind:         KindMessage,
		SourceID:     "chat_reply_c2_u1",
		TargetUserID: "u1",
		Title:        "Atlas replied",
		BodyMD:       "first reply",
		SenderType:   "agent",
		SenderID:     "a1",
		SenderName:   "Atlas",
	}
	if err := UpsertMessage(ctx, db, quietLogger(), item); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// Simulate the user having read (or resolved) the first notification.
	if _, err := db.Exec(`UPDATE inbox_items SET state='resolved', resolved_at='2026-07-01T00:00:00Z',
		resolved_action='dismissed' WHERE source_id='chat_reply_c2_u1'`); err != nil {
		t.Fatalf("mark resolved: %v", err)
	}

	item.BodyMD = "second reply"
	if err := UpsertMessage(ctx, db, quietLogger(), item); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message' AND source_id='chat_reply_c2_u1'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want exactly 1 (dedupe per user+chat)", n)
	}
	var state, body string
	var resolvedAt any
	if err := db.QueryRow(`SELECT state, body_md, resolved_at FROM inbox_items WHERE source_id='chat_reply_c2_u1'`).
		Scan(&state, &body, &resolvedAt); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if state != "unread" {
		t.Errorf("state = %q, want unread (new reply resurrects the item)", state)
	}
	if body != "second reply" {
		t.Errorf("body_md = %q, want refreshed preview", body)
	}
	if resolvedAt != nil {
		t.Errorf("resolved_at = %v, want NULL after resurrect", resolvedAt)
	}
}

// TestUpsertMessage_SecondCallClearsPerUserReadMarkers is the A7 half of
// TestUpsertMessage_SecondCallRefreshesInsteadOfDuplicating: a resurrected
// item carries new content, so a per-user inbox_item_reads row left over
// from the FIRST occurrence must not make the refreshed row look already
// read to the user who read the old one.
func TestUpsertMessage_SecondCallClearsPerUserReadMarkers(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	// newInboxTestDB already seeds workspace 'ws1'.
	if _, err := db.Exec(`INSERT INTO users (id, email) VALUES ('u1','u1@example.com')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	item := Item{
		WorkspaceID:  "ws1",
		Kind:         KindMessage,
		SourceID:     "chat_reply_c3_u1",
		TargetUserID: "u1",
		Title:        "Atlas replied",
		BodyMD:       "first reply",
		SenderType:   "agent",
		SenderID:     "a1",
		SenderName:   "Atlas",
	}
	if err := UpsertMessage(ctx, db, quietLogger(), item); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var itemID string
	if err := db.QueryRow(`SELECT id FROM inbox_items WHERE source_id='chat_reply_c3_u1'`).Scan(&itemID); err != nil {
		t.Fatalf("read id: %v", err)
	}
	// u1 reads the first occurrence.
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, 'u1')`, itemID); err != nil {
		t.Fatalf("seed per-user read marker: %v", err)
	}

	item.BodyMD = "second reply"
	if err := UpsertMessage(ctx, db, quietLogger(), item); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ?`, itemID).Scan(&n); err != nil {
		t.Fatalf("count read markers: %v", err)
	}
	if n != 0 {
		t.Errorf("per-user read markers survived a resurrect = %d, want 0 (the new occurrence must read as unread again)", n)
	}
}

func TestUpsertMessage_ValidationNoop(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	// Missing source id → silent no-op, no error, no row (same contract
	// as Insert: caller bug, not transient SQL failure).
	if err := UpsertMessage(context.Background(), db, quietLogger(), Item{
		WorkspaceID: "ws1", Kind: KindMessage,
	}); err != nil {
		t.Fatalf("want nil error on validation no-op, got %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
}
