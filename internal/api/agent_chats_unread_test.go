package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// agent_chats_unread_test.go covers the per-session unread / last-activity
// surface: ListChats returning last_activity_at + unread_count for the
// requesting user, and MarkChatRead (PUT /agents/{id}/chats/{id}/read)
// advancing the read cursor + clearing the paired inbox notification.
// Helpers prefixed unreadT.

func unreadTSeed(t *testing.T, db *sql.DB) (wsID, userID string) {
	t.Helper()
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('unread-crew', ?, 'C', 'unread-c')`, wsID)
	seedAgentRow(t, db, "unread-ag", wsID, "unread-crew", "Atlas", "atlas", "AGENT")
	return wsID, userID
}

func unreadTChat(t *testing.T, db *sql.DB, chatID, wsID, userID string) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, created_by, status, message_count)
		VALUES (?, 'unread-ag', ?, ?, 'ACTIVE', 2)`, chatID, wsID, userID)
}

func unreadTMsg(t *testing.T, db *sql.DB, id, chatID, role, ts string, author any) {
	t.Helper()
	execOrFatal(t, db, `INSERT INTO conversation_messages (id, session_id, agent_id, role, content, ts, author_user_id)
		VALUES (?, ?, 'unread-ag', ?, 'msg body', ?, ?)`, id, chatID, role, ts, author)
}

type chatUnreadRow struct {
	ID             string `json:"id"`
	LastActivityAt string `json:"last_activity_at"`
	UnreadCount    int    `json:"unread_count"`
}

func unreadTList(t *testing.T, h *AgentHandler, wsID, userID string) []chatUnreadRow {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/agents/unread-ag/chats", nil)
	req.SetPathValue("agentId", "unread-ag")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.ListChats(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var rows []chatUnreadRow
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}

func unreadTMarkRead(t *testing.T, h *AgentHandler, agentID, chatID, wsID, userID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("PUT", "/api/v1/agents/"+agentID+"/chats/"+chatID+"/read", nil)
	req.SetPathValue("agentId", agentID)
	req.SetPathValue("chatId", chatID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.MarkChatRead(rr, req)
	return rr
}

// A chat with an assistant reply the user has never read reports
// unread_count=1 (the user's own message doesn't count) and carries a
// non-empty last_activity_at.
func TestListChats_UnreadCountAndLastActivity(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	unreadTChat(t, db, "chat-u1", wsID, userID)
	unreadTMsg(t, db, "m1", "chat-u1", "user", "2026-07-01T10:00:00.000Z", userID)
	unreadTMsg(t, db, "m2", "chat-u1", "assistant", "2026-07-01T10:00:05.000Z", nil)

	h := NewAgentHandler(sdb, newTestLogger())
	rows := unreadTList(t, h, wsID, userID)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].UnreadCount != 1 {
		t.Errorf("unread_count = %d, want 1 (assistant reply only)", rows[0].UnreadCount)
	}
	if rows[0].LastActivityAt == "" {
		t.Error("last_activity_at empty, want non-empty")
	}
}

// Ordering is by last activity, not creation: an older chat with a newer
// last_activity_at must sort first.
func TestListChats_OrderedByLastActivity(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	unreadTChat(t, db, "chat-old", wsID, userID)
	unreadTChat(t, db, "chat-new", wsID, userID)
	// chat-old was created first but has the most recent activity.
	execOrFatal(t, sdb, `UPDATE chats SET created_at = '2026-01-01 00:00:00', started_at = '2026-01-01 00:00:00',
		last_activity_at = '2026-07-01T12:00:00.000Z' WHERE id = 'chat-old'`)
	execOrFatal(t, sdb, `UPDATE chats SET created_at = '2026-06-01 00:00:00', started_at = '2026-06-01 00:00:00',
		last_activity_at = '2026-06-01T00:00:00.000Z' WHERE id = 'chat-new'`)

	h := NewAgentHandler(sdb, newTestLogger())
	rows := unreadTList(t, h, wsID, userID)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ID != "chat-old" {
		t.Errorf("first row = %s, want chat-old (most recent activity)", rows[0].ID)
	}
}

// Marking a chat read advances the cursor: the same list call then
// reports unread_count=0, and a subsequent (newer) assistant message
// flips it back to 1.
func TestMarkChatRead_ResetsUnread(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	unreadTChat(t, db, "chat-r1", wsID, userID)
	unreadTMsg(t, db, "m1", "chat-r1", "assistant", "2026-07-01T10:00:00.000Z", nil)

	h := NewAgentHandler(sdb, newTestLogger())
	if got := unreadTList(t, h, wsID, userID)[0].UnreadCount; got != 1 {
		t.Fatalf("pre-read unread = %d, want 1", got)
	}

	rr := unreadTMarkRead(t, h, "unread-ag", "chat-r1", wsID, userID)
	if rr.Code != http.StatusOK {
		t.Fatalf("mark read status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if got := unreadTList(t, h, wsID, userID)[0].UnreadCount; got != 0 {
		t.Fatalf("post-read unread = %d, want 0", got)
	}

	// New assistant message after the cursor → unread again. The mark-read
	// cursor is written with millisecond precision; use a clearly-later ts.
	unreadTMsg(t, db, "m2", "chat-r1", "assistant", "2126-01-01T00:00:00.000Z", nil)
	if got := unreadTList(t, h, wsID, userID)[0].UnreadCount; got != 1 {
		t.Fatalf("after new reply unread = %d, want 1", got)
	}
}

// Cross-workspace / wrong-agent mark-read must 404 (no cursor row written).
func TestMarkChatRead_WrongScope404(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	unreadTChat(t, db, "chat-s1", wsID, userID)

	h := NewAgentHandler(sdb, newTestLogger())

	// Wrong agent id.
	if rr := unreadTMarkRead(t, h, "other-agent", "chat-s1", wsID, userID); rr.Code != http.StatusNotFound {
		t.Errorf("wrong agent: status = %d, want 404", rr.Code)
	}
	// Wrong workspace.
	if rr := unreadTMarkRead(t, h, "unread-ag", "chat-s1", "other-ws", userID); rr.Code != http.StatusNotFound {
		t.Errorf("wrong workspace: status = %d, want 404", rr.Code)
	}
	var n int
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM chat_read_cursors`).Scan(&n); err != nil {
		t.Fatalf("count cursors: %v", err)
	}
	if n != 0 {
		t.Errorf("cursor rows = %d, want 0 after rejected mark-reads", n)
	}
}

// Behaviour lock for the ListChats unread/last-activity projection
// (#1255 item 6). This is NOT a regression test for a defect — it pins the
// exact contract of the per-row unread computation so the query can be
// rewritten from a correlated subquery to a grouped LEFT JOIN without
// silently changing results.
//
// Four cases in one list call, so the aggregation is exercised across a
// mixed result set rather than one row at a time:
//
//	a) chat with zero messages          → unread 0
//	b) chat whose messages are all read → unread 0
//	c) chat with some unread            → unread 1
//	d) chat with everything unread      → unread 2 (own user message excluded)
//
// It also asserts last_activity_at *by value* on every row, including the
// zero-message chat that falls back through COALESCE to a legacy
// space-separated started_at. A GROUP BY that collapsed or mis-picked that
// expression would show up here, as would any change to the DESC ordering.
func TestListChats_UnreadProjectionBehaviourLock(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)

	// (a) zero messages, no last_activity_at → COALESCE falls back to the
	// legacy space-separated started_at, normalised by strftime.
	unreadTChat(t, db, "lock-a-empty", wsID, userID)
	execOrFatal(t, db, `UPDATE chats SET last_activity_at = NULL,
		started_at = '2026-05-01 00:00:00' WHERE id = 'lock-a-empty'`)

	// (b) two messages, cursor past both.
	unreadTChat(t, db, "lock-b-read", wsID, userID)
	execOrFatal(t, db, `UPDATE chats SET last_activity_at = '2026-06-01T10:00:05.000Z' WHERE id = 'lock-b-read'`)
	unreadTMsg(t, db, "lock-b-m1", "lock-b-read", "assistant", "2026-06-01T10:00:00.000Z", nil)
	unreadTMsg(t, db, "lock-b-m2", "lock-b-read", "assistant", "2026-06-01T10:00:05.000Z", nil)
	execOrFatal(t, db, `INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES (?, 'lock-b-read', '2026-06-01T23:59:59.999Z')`, userID)

	// (c) three messages, cursor between the second and the third.
	unreadTChat(t, db, "lock-c-partial", wsID, userID)
	execOrFatal(t, db, `UPDATE chats SET last_activity_at = '2026-07-01T10:00:10.000Z' WHERE id = 'lock-c-partial'`)
	unreadTMsg(t, db, "lock-c-m1", "lock-c-partial", "assistant", "2026-07-01T10:00:00.000Z", nil)
	unreadTMsg(t, db, "lock-c-m2", "lock-c-partial", "assistant", "2026-07-01T10:00:05.000Z", nil)
	unreadTMsg(t, db, "lock-c-m3", "lock-c-partial", "assistant", "2026-07-01T10:00:10.000Z", nil)
	execOrFatal(t, db, `INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES (?, 'lock-c-partial', '2026-07-01T10:00:06.000Z')`, userID)

	// (d) no cursor at all → everything counts, except the caller's own
	// user-role message (author guard).
	unreadTChat(t, db, "lock-d-unread", wsID, userID)
	execOrFatal(t, db, `UPDATE chats SET last_activity_at = '2026-08-01T10:00:00.000Z' WHERE id = 'lock-d-unread'`)
	unreadTMsg(t, db, "lock-d-m1", "lock-d-unread", "user", "2026-08-01T09:59:00.000Z", userID)
	unreadTMsg(t, db, "lock-d-m2", "lock-d-unread", "assistant", "2026-08-01T09:59:30.000Z", nil)
	unreadTMsg(t, db, "lock-d-m3", "lock-d-unread", "assistant", "2026-08-01T10:00:00.000Z", nil)

	h := NewAgentHandler(sdb, newTestLogger())
	rows := unreadTList(t, h, wsID, userID)

	want := []chatUnreadRow{
		{ID: "lock-d-unread", LastActivityAt: "2026-08-01T10:00:00.000Z", UnreadCount: 2},
		{ID: "lock-c-partial", LastActivityAt: "2026-07-01T10:00:10.000Z", UnreadCount: 1},
		{ID: "lock-b-read", LastActivityAt: "2026-06-01T10:00:05.000Z", UnreadCount: 0},
		{ID: "lock-a-empty", LastActivityAt: "2026-05-01T00:00:00.000Z", UnreadCount: 0},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d (%+v)", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].ID != w.ID {
			t.Fatalf("row %d id = %q, want %q (ordering by last_activity_at DESC broke): %+v",
				i, rows[i].ID, w.ID, rows)
		}
		if rows[i].UnreadCount != w.UnreadCount {
			t.Errorf("%s: unread_count = %d, want %d", w.ID, rows[i].UnreadCount, w.UnreadCount)
		}
		if rows[i].LastActivityAt != w.LastActivityAt {
			t.Errorf("%s: last_activity_at = %q, want %q", w.ID, rows[i].LastActivityAt, w.LastActivityAt)
		}
	}
}

// A second user's cursor must not affect the caller's counts, and the
// caller's own cursor must not leak across chats — the grouped join adds a
// second joined table, so pin the per-(user, chat) scoping explicitly.
func TestListChats_UnreadIsPerUser(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	otherID := "unread-other-user"
	execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'Other User')`, otherID)

	unreadTChat(t, db, "peruser-c1", wsID, userID)
	unreadTMsg(t, db, "peruser-m1", "peruser-c1", "assistant", "2026-07-01T10:00:00.000Z", nil)
	unreadTMsg(t, db, "peruser-m2", "peruser-c1", "assistant", "2026-07-01T10:00:05.000Z", nil)
	// The other user has read everything; the caller has read nothing.
	execOrFatal(t, db, `INSERT INTO chat_read_cursors (user_id, chat_id, last_read_at)
		VALUES (?, 'peruser-c1', '2026-07-02T00:00:00.000Z')`, otherID)

	h := NewAgentHandler(sdb, newTestLogger())
	if got := unreadTList(t, h, wsID, userID)[0].UnreadCount; got != 2 {
		t.Errorf("caller unread = %d, want 2 (other user's cursor must not apply)", got)
	}
	if got := unreadTList(t, h, wsID, otherID)[0].UnreadCount; got != 0 {
		t.Errorf("other-user unread = %d, want 0", got)
	}
}

// Marking read also clears the "agent replied" inbox notification for
// this (user, chat) pair — the deep-linked item must not stay unread
// after the user has actually seen the reply.
func TestMarkChatRead_ClearsInboxNotification(t *testing.T) {
	sdb := setupTestDB(t)
	db := sdb
	wsID, userID := unreadTSeed(t, db)
	unreadTChat(t, db, "chat-n1", wsID, userID)
	execOrFatal(t, sdb, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, target_user_id, title, state, blocking)
		VALUES ('ibx_message_chat_reply_chat-n1_'||?, ?, 'message', 'chat_reply_chat-n1_'||?, ?, 'Atlas replied', 'unread', 0)`,
		userID, wsID, userID, userID)

	h := NewAgentHandler(sdb, newTestLogger())
	if rr := unreadTMarkRead(t, h, "unread-ag", "chat-n1", wsID, userID); rr.Code != http.StatusOK {
		t.Fatalf("mark read status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var state string
	if err := sdb.QueryRow(`SELECT state FROM inbox_items WHERE source_id = 'chat_reply_chat-n1_'||?`, userID).Scan(&state); err != nil {
		t.Fatalf("read inbox item: %v", err)
	}
	if state != "read" {
		t.Errorf("inbox item state = %q, want read", state)
	}
}
