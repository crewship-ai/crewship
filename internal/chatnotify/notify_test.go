package chatnotify

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/database"
	_ "modernc.org/sqlite"
)

var notifyTestCounter atomic.Int64

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newNotifyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("crewship-chatnotify-test-%d", notifyTestCounter.Add(1))
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", name)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(context.Background(), db, quietLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seed := []string{
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'W', 'w')`,
		`INSERT INTO users (id, email, full_name) VALUES ('u1', 'u1@x.io', 'U One')`,
		`INSERT INTO users (id, email, full_name) VALUES ('u2', 'u2@x.io', 'U Two')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr1', 'ws1', 'C', 'c')`,
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role) VALUES ('ag1', 'ws1', 'cr1', 'Atlas', 'atlas', 'AGENT')`,
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, status) VALUES ('c1', 'ag1', 'ws1', 'u1', 'Deploy help', 'ACTIVE')`,
	}
	for _, q := range seed {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	return db
}

// fakeHub scripts the "is the user watching the session channel" answer
// and records inbox.updated broadcasts.
type fakeHub struct {
	subscribed map[string]bool // key: channel + "|" + userID
	broadcasts []string        // workspace ids that got inbox.updated
}

func (f *fakeHub) IsUserSubscribed(channel, userID string) bool {
	return f.subscribed[channel+"|"+userID]
}

func (f *fakeHub) BroadcastWorkspace(wsID, eventType string, payload any) {
	if eventType == "inbox.updated" {
		f.broadcasts = append(f.broadcasts, wsID)
	}
}

func baseNotification() chatbridge.ReplyNotification {
	return chatbridge.ReplyNotification{
		ChatID:       "c1",
		WorkspaceID:  "ws1",
		AgentID:      "ag1",
		AgentSlug:    "atlas",
		Visibility:   "private",
		AuthorUserID: "u1",
		ReplyText:    "Deploy finished — 3 services green.",
	}
}

func TestNotify_CreatorGetsInboxItemWhenNotWatching(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	n.NotifyAssistantReply(context.Background(), baseNotification())

	var target, title, body, state, payload string
	err := db.QueryRow(`SELECT target_user_id, title, body_md, state, payload_json
		FROM inbox_items WHERE kind='message' AND source_id='chat_reply_c1_u1'`).
		Scan(&target, &title, &body, &state, &payload)
	if err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	if target != "u1" || state != "unread" {
		t.Errorf("target=%q state=%q, want u1/unread", target, state)
	}
	if !strings.Contains(title, "Atlas") {
		t.Errorf("title %q should carry the agent name", title)
	}
	if !strings.Contains(body, "Deploy finished") {
		t.Errorf("body %q should carry the reply preview", body)
	}
	if !strings.Contains(payload, `"chat_url":"/chat/atlas?session=c1"`) {
		t.Errorf("payload %q missing deep link", payload)
	}
	if len(hub.broadcasts) != 1 || hub.broadcasts[0] != "ws1" {
		t.Errorf("broadcasts = %v, want one inbox.updated on ws1", hub.broadcasts)
	}
}

func TestNotify_SkipsUserWatchingTheSession(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{"session:c1|u1": true}}
	n := New(db, hub, quietLogger())

	n.NotifyAssistantReply(context.Background(), baseNotification())

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("inbox rows = %d, want 0 (user is watching live)", count)
	}
	if len(hub.broadcasts) != 0 {
		t.Errorf("broadcasts = %v, want none", hub.broadcasts)
	}
}

func TestNotify_GroupChatNotifiesParticipantsExceptAuthor(t *testing.T) {
	db := newNotifyTestDB(t)
	if _, err := db.Exec(`UPDATE chats SET visibility='group' WHERE id='c1'`); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO chat_participants (chat_id, user_id, role) VALUES ('c1','u2','member')`); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	rn := baseNotification()
	rn.Visibility = "group"
	rn.AuthorUserID = "u1" // u1 sent the @mention that triggered the reply
	n.NotifyAssistantReply(context.Background(), rn)

	rows, err := db.Query(`SELECT target_user_id FROM inbox_items WHERE kind='message' ORDER BY target_user_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatalf("scan: %v", err)
		}
		targets = append(targets, u)
	}
	if len(targets) != 1 || targets[0] != "u2" {
		t.Errorf("targets = %v, want [u2] (author u1 excluded in group chat)", targets)
	}
}

func TestNotify_SecondReplyUpdatesExistingItem(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	n.NotifyAssistantReply(context.Background(), baseNotification())
	rn := baseNotification()
	rn.ReplyText = "Follow-up: logs attached."
	n.NotifyAssistantReply(context.Background(), rn)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='message'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("rows = %d, want 1 (dedupe per user+chat)", count)
	}
	var body string
	if err := db.QueryRow(`SELECT body_md FROM inbox_items WHERE source_id='chat_reply_c1_u1'`).Scan(&body); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(body, "Follow-up") {
		t.Errorf("body %q, want refreshed preview", body)
	}
}

func TestNotify_PreviewTruncatedAndScrubbed(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	rn := baseNotification()
	rn.ReplyText = "token is ghp_abcdefghijklmnopqrstuvwxyz0123456789 " + strings.Repeat("x", 500)
	n.NotifyAssistantReply(context.Background(), rn)

	var body string
	if err := db.QueryRow(`SELECT body_md FROM inbox_items WHERE source_id='chat_reply_c1_u1'`).Scan(&body); err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(body, "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("preview leaked a credential: %q", body)
	}
	if got := len([]rune(body)); got > 140 {
		t.Errorf("preview length = %d runes, want ≤140 (120 + ellipsis headroom)", got)
	}
}

// A recipient whose read cursor already covers the reply timestamp must
// NOT get a bell item — this is the mark-read vs in-flight-notifier race:
// if MarkChatRead commits before the notifier runs, the upsert would
// otherwise resurrect an item the cursor already supersedes.
func TestNotify_SkipsRecipientWithFreshCursor(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	rn := baseNotification()
	rn.RepliedAt = mustTime(t, "2026-07-02T10:00:00.500Z")
	// Cursor at the exact same millisecond as the reply — >= must absorb
	// the boundary (the mark-read that raced us saw this reply).
	if _, err := db.Exec(`INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES ('u1', 'c1', '2026-07-02T10:00:00.500Z')`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	n.NotifyAssistantReply(context.Background(), rn)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("inbox rows = %d, want 0 (cursor >= reply timestamp)", count)
	}
	if len(hub.broadcasts) != 0 {
		t.Errorf("broadcasts = %v, want none", hub.broadcasts)
	}
}

// A stale cursor (older than the reply) must still notify — the skip is
// strictly "cursor covers this reply", never "cursor exists".
func TestNotify_NotifiesRecipientWithStaleCursor(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	rn := baseNotification()
	rn.RepliedAt = mustTime(t, "2026-07-02T10:00:00.500Z")
	if _, err := db.Exec(`INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES ('u1', 'c1', '2026-07-02T09:59:59.000Z')`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	n.NotifyAssistantReply(context.Background(), rn)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE target_user_id='u1'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("inbox rows = %d, want 1 (cursor is older than the reply)", count)
	}
}

func mustTime(t *testing.T, iso string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02T15:04:05.000Z", iso)
	if err != nil {
		t.Fatalf("parse %q: %v", iso, err)
	}
	return ts.UTC()
}

func TestNotify_EmptyReplyOrMissingChatIsNoop(t *testing.T) {
	db := newNotifyTestDB(t)
	hub := &fakeHub{subscribed: map[string]bool{}}
	n := New(db, hub, quietLogger())

	rn := baseNotification()
	rn.ReplyText = "   "
	n.NotifyAssistantReply(context.Background(), rn)

	rn = baseNotification()
	rn.ChatID = "nope"
	n.NotifyAssistantReply(context.Background(), rn)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rows = %d, want 0", count)
	}
}
