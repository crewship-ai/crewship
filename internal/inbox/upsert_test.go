package inbox

import (
	"context"
	"testing"
)

// UpsertMessage is the dedupe primitive behind the "your agent replied"
// notification: one row per (workspace, kind, source_id); a second reply
// refreshes title/body/timestamps and resurrects the row as unread
// instead of piling up siblings.

// #2274: the dedupe key is workspace-scoped, so the same (kind, source_id)
// may exist in two workspaces — a fork is the guaranteed producer. The
// derived row id carries the workspace too, or the second workspace's
// INSERT OR IGNORE would eat its row on the PK collision with the first.
func TestInsert_TwoWorkspacesSameSource_BothLand(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u1', 'u1@e2e.test', 'One')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws2', 'Ws2', 'ws2')`); err != nil {
		t.Fatalf("seed workspace 2: %v", err)
	}
	ctx := context.Background()

	item := func(ws string) Item {
		return Item{
			WorkspaceID:  ws,
			Kind:         KindMessage,
			SourceID:     "chat_reply_shared_source",
			TargetUserID: "",
			Title:        "reply in " + ws,
			SenderType:   "agent",
		}
	}
	for _, ws := range []string{"ws1", "ws2"} {
		if err := Insert(ctx, db, quietLogger(), item(ws)); err != nil {
			t.Fatalf("insert into %s: %v", ws, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message' AND source_id='chat_reply_shared_source'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows across both workspaces = %d, want 2 — one workspace's row was eaten by a PK collision", n)
	}
	// Upsert refreshes only its own workspace's row.
	refreshed := item("ws1")
	refreshed.Title = "refreshed in ws1"
	if err := UpsertMessage(ctx, db, quietLogger(), refreshed); err != nil {
		t.Fatalf("upsert ws1: %v", err)
	}
	var ws1Title, ws2Title string
	if err := db.QueryRow(`SELECT title FROM inbox_items WHERE workspace_id='ws1' AND source_id='chat_reply_shared_source'`).Scan(&ws1Title); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT title FROM inbox_items WHERE workspace_id='ws2' AND source_id='chat_reply_shared_source'`).Scan(&ws2Title); err != nil {
		t.Fatal(err)
	}
	if ws1Title != "refreshed in ws1" || ws2Title != "reply in ws2" {
		t.Errorf("titles = (%q, %q) — the upsert must touch only its own workspace", ws1Title, ws2Title)
	}
	// And the marker cleanup resolves the row by the key: markers of the
	// OTHER workspace's item must survive an upsert here.
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id, read_at)
		SELECT id, 'u1', '2026-09-24T12:00:00Z' FROM inbox_items WHERE workspace_id='ws2'`); err != nil {
		t.Fatal(err)
	}
	if err := UpsertMessage(ctx, db, quietLogger(), refreshed); err != nil {
		t.Fatalf("upsert ws1 again: %v", err)
	}
	var ws2Markers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads r JOIN inbox_items i ON i.id=r.inbox_item_id
		WHERE i.workspace_id='ws2'`).Scan(&ws2Markers); err != nil {
		t.Fatal(err)
	}
	if ws2Markers != 1 {
		t.Errorf("ws2 read markers = %d, want 1 — the marker cleanup deleted another workspace's markers", ws2Markers)
	}
}

func TestUpsertMessage_InsertsNewRow(t *testing.T) {
	t.Parallel()
	db := newInboxTestDB(t)
	ctx := context.Background()

	if err := UpsertMessage(ctx, db, quietLogger(), Item{
		WorkspaceID:  "ws1",
		Kind:         KindMessage,
		SourceID:     "chat_reply_c1_u1",
		TargetUserID: "",
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
		TargetUserID: "",
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
		TargetUserID: "",
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
