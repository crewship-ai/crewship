package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// message_reactions.go — chat-message emoji reactions CRUD.
//
// Tenancy gate is non-standard here: the routes mount under
// /api/v1/chats/{chatId}/... without a workspace_id, so the handler
// derives the workspace from the chat and verifies workspace membership
// itself. These tests exercise that gate plus the 401/400 ordering
// (Add/Remove: 401 BEFORE 404 so a logged-out caller cannot probe
// chat existence) and the INSERT OR IGNORE idempotency of Add.
// ---------------------------------------------------------------------------

func newReactionsTestHandler(t *testing.T) *MessageReactionsHandler {
	t.Helper()
	db := setupTestDB(t)
	return NewMessageReactionsHandler(db, newTestLogger())
}

// reactionsTestBed seeds the workspace + crew + agent + chat + member
// rows the reactions handler depends on, and returns identifiers for the
// happy-path member, the chat, and an isolated workspace whose member
// must NOT be able to see the chat.
type reactionsTestBed struct {
	h         *MessageReactionsHandler
	userID    string
	otherID   string // user in a different workspace
	wsID      string
	chatID    string
	messageID string
}

func setupReactionsTestBed(t *testing.T) *reactionsTestBed {
	t.Helper()
	h := newReactionsTestHandler(t)
	userID := seedTestUser(t, h.db)
	// seedTestWorkspace already inserts a workspace_members row for userID
	// as OWNER — ensureChatVisible's membership check is satisfied.
	wsID := seedTestWorkspace(t, h.db, userID)

	seedCrewRow(t, h.db, "crew-r", wsID, "C", "c-react")
	seedAgentRow(t, h.db, "agent-r", wsID, "crew-r", "A", "a-react", "AGENT")

	chatID := "chat-react"
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, 'agent-r', ?, ?, 'r')`, chatID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// Second user in a different workspace — used for cross-tenant gate tests.
	otherID := "user-other-react"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@x.com', 'Other')`, otherID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	otherWS := "ws-other-react"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o-react')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ('m-other', ?, ?, 'OWNER')`, otherWS, otherID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	return &reactionsTestBed{
		h:         h,
		userID:    userID,
		otherID:   otherID,
		wsID:      wsID,
		chatID:    chatID,
		messageID: "msg-1",
	}
}

func reactionReq(method, url, body, userID string) *http.Request {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	}
	return req
}

// ---- List ----

func TestReactions_List_HappyAndMineFlag(t *testing.T) {
	bed := setupReactionsTestBed(t)
	// Two users react: bed.userID with 👍 + 🎉, otherID also with 👍.
	// (otherID isn't in this workspace, but the reaction row itself is
	// allowed — the visibility check is per-CHAT not per-REACTION-author.
	// The "mine" flag is what matters: it must be true for emojis the
	// caller themselves reacted with.)
	if _, err := bed.h.db.Exec(`INSERT INTO message_reactions (id, chat_id, message_id, emoji, user_id) VALUES
		('r1', ?, ?, '👍', ?), ('r2', ?, ?, '🎉', ?), ('r3', ?, ?, '👍', ?)`,
		bed.chatID, bed.messageID, bed.userID,
		bed.chatID, bed.messageID, bed.userID,
		bed.chatID, bed.messageID, bed.otherID); err != nil {
		t.Fatalf("seed reactions: %v", err)
	}

	req := reactionReq("GET", "/api/v1/chats/"+bed.chatID+"/messages/"+bed.messageID+"/reactions", "", bed.userID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Reactions []reactionRow `json:"reactions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Reactions) != 2 {
		t.Fatalf("got %d reactions, want 2", len(body.Reactions))
	}
	// Sort: count DESC, emoji ASC. 👍 has 2, 🎉 has 1.
	if body.Reactions[0].Emoji != "👍" || body.Reactions[0].Count != 2 || !body.Reactions[0].Mine {
		t.Errorf("first reaction = %+v, want {👍 2 true}", body.Reactions[0])
	}
	if body.Reactions[1].Emoji != "🎉" || body.Reactions[1].Count != 1 || !body.Reactions[1].Mine {
		t.Errorf("second reaction = %+v, want {🎉 1 true}", body.Reactions[1])
	}
}

func TestReactions_List_NotMemberOfChatWorkspace_404(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("GET", "/api/v1/chats/"+bed.chatID+"/messages/"+bed.messageID+"/reactions", "", bed.otherID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace list = %d, want 404 (no existence leak)", rr.Code)
	}
}

func TestReactions_List_NoAuth_404(t *testing.T) {
	// List does NOT 401 on missing user — it returns 404 (chat not found)
	// because ensureChatVisible bails on a nil user. That's the source's
	// stated behavior; this test pins it so a refactor doesn't accidentally
	// leak chat existence to anonymous probes.
	bed := setupReactionsTestBed(t)
	req := reactionReq("GET", "/api/v1/chats/"+bed.chatID+"/messages/"+bed.messageID+"/reactions", "", "")
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("anonymous list = %d, want 404 (chat existence hidden)", rr.Code)
	}
}

func TestReactions_List_UnknownChat_404(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("GET", "/api/v1/chats/missing/messages/m/reactions", "", bed.userID)
	req.SetPathValue("chatId", "missing")
	req.SetPathValue("messageId", "m")
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown chat list = %d, want 404", rr.Code)
	}
}

func TestReactions_List_EmptyMessage_ReturnsEmptyArray(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("GET", "/api/v1/chats/"+bed.chatID+"/messages/never-reacted/reactions", "", bed.userID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", "never-reacted")
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// Decode the response and assert reactions is a non-nil empty slice —
	// the UI iterates over it, and `null` (which json.Unmarshal would
	// accept silently for a []T target) would crash on the JS side.
	// A substring check was order-sensitive; a structured assertion
	// catches a key-order regression OR a marshaller that emitted null.
	var body struct {
		Reactions []map[string]any `json:"reactions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	if body.Reactions == nil {
		t.Errorf("reactions = null; want non-nil empty array (UI iterates)")
	}
	if len(body.Reactions) != 0 {
		t.Errorf("reactions = %+v, want empty array", body.Reactions)
	}
}

// ---- Add ----

func TestReactions_Add_NoAuth_401_NotProbing(t *testing.T) {
	// Critical: Add must 401 BEFORE checking chat existence, so an
	// anonymous attacker can't enumerate chat IDs via the response code.
	bed := setupReactionsTestBed(t)
	req := reactionReq("POST", "/api/v1/chats/"+bed.chatID+"/messages/"+bed.messageID+"/reactions", `{"emoji":"👍"}`, "")
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	rr := httptest.NewRecorder()
	bed.h.Add(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous Add = %d, want 401 (must come before 404)", rr.Code)
	}
}

func TestReactions_Add_CrossWorkspace_404(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("POST", "/api/v1/chats/"+bed.chatID+"/messages/"+bed.messageID+"/reactions", `{"emoji":"👍"}`, bed.otherID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	rr := httptest.NewRecorder()
	bed.h.Add(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace Add = %d, want 404", rr.Code)
	}
}

func TestReactions_Add_BadBody_400(t *testing.T) {
	bed := setupReactionsTestBed(t)

	cases := []struct {
		name, body string
	}{
		{"invalid-json", `not-json`},
		{"missing-emoji", `{"emoji":""}`},
		{"emoji-too-long", `{"emoji":"👍👍👍👍👍👍👍👍👍"}`}, // 9 runes
		{"html-injection", `{"emoji":"<img onerror=x>"}`},
		{"ascii-letters", `{"emoji":"hi"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := reactionReq("POST", "/x", tc.body, bed.userID)
			req.SetPathValue("chatId", bed.chatID)
			req.SetPathValue("messageId", bed.messageID)
			rr := httptest.NewRecorder()
			bed.h.Add(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: code = %d, want 400", tc.name, rr.Code)
			}
		})
	}
}

func TestReactions_Add_IdempotentOnDuplicate(t *testing.T) {
	// UNIQUE(chat_id, message_id, emoji, user_id) + INSERT OR IGNORE
	// means hitting Add twice with the same emoji must NOT 500 and must
	// leave exactly one row.
	bed := setupReactionsTestBed(t)
	for i := 0; i < 2; i++ {
		req := reactionReq("POST", "/x", `{"emoji":"👍"}`, bed.userID)
		req.SetPathValue("chatId", bed.chatID)
		req.SetPathValue("messageId", bed.messageID)
		rr := httptest.NewRecorder()
		bed.h.Add(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("Add #%d code = %d body=%s, want 204", i+1, rr.Code, rr.Body.String())
		}
	}
	var n int
	if err := bed.h.db.QueryRow(`SELECT COUNT(*) FROM message_reactions WHERE chat_id = ? AND message_id = ? AND user_id = ? AND emoji = '👍'`,
		bed.chatID, bed.messageID, bed.userID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("duplicate Add produced %d rows, want 1 (idempotent INSERT OR IGNORE)", n)
	}
}

// ---- Remove ----

func TestReactions_Remove_NoAuth_401_NotProbing(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("DELETE", "/x", "", "")
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	req.SetPathValue("emoji", "👍")
	rr := httptest.NewRecorder()
	bed.h.Remove(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous Remove = %d, want 401 (must come before 404)", rr.Code)
	}
}

func TestReactions_Remove_CrossWorkspace_404(t *testing.T) {
	bed := setupReactionsTestBed(t)
	req := reactionReq("DELETE", "/x", "", bed.otherID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	req.SetPathValue("emoji", "👍")
	rr := httptest.NewRecorder()
	bed.h.Remove(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace Remove = %d, want 404", rr.Code)
	}
}

func TestReactions_Remove_OnlyRemovesCallersOwnRow(t *testing.T) {
	bed := setupReactionsTestBed(t)
	// Seed two reactions on the same (chat, message, emoji) — one from
	// the caller, one from a different user. The DELETE statement scopes
	// to user_id, so other-user's row must remain untouched.
	if _, err := bed.h.db.Exec(`INSERT INTO message_reactions (id, chat_id, message_id, emoji, user_id) VALUES
		('mine', ?, ?, '👍', ?), ('theirs', ?, ?, '👍', ?)`,
		bed.chatID, bed.messageID, bed.userID,
		bed.chatID, bed.messageID, bed.otherID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := reactionReq("DELETE", "/x", "", bed.userID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	req.SetPathValue("emoji", "👍")
	rr := httptest.NewRecorder()
	bed.h.Remove(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("Remove code = %d, want 204", rr.Code)
	}
	var remaining int
	if err := bed.h.db.QueryRow(`SELECT COUNT(*) FROM message_reactions WHERE chat_id = ? AND message_id = ? AND emoji = '👍'`,
		bed.chatID, bed.messageID).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Errorf("remaining reactions = %d, want 1 (other user's row must survive)", remaining)
	}
	// Confirm the surviving row is the other user's
	var who string
	if err := bed.h.db.QueryRow(`SELECT user_id FROM message_reactions WHERE chat_id = ? AND message_id = ? AND emoji = '👍'`,
		bed.chatID, bed.messageID).Scan(&who); err != nil {
		t.Fatalf("scan survivor: %v", err)
	}
	if who != bed.otherID {
		t.Errorf("survivor user_id = %s, want %s", who, bed.otherID)
	}
}

func TestReactions_Remove_Idempotent_NoSuchRow_204(t *testing.T) {
	// Removing a reaction that doesn't exist is a successful no-op — UX
	// requirement: the bell-style toggle calls Remove on every untoggle
	// regardless of prior state.
	bed := setupReactionsTestBed(t)
	req := reactionReq("DELETE", "/x", "", bed.userID)
	req.SetPathValue("chatId", bed.chatID)
	req.SetPathValue("messageId", bed.messageID)
	req.SetPathValue("emoji", "👍")
	rr := httptest.NewRecorder()
	bed.h.Remove(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("noop Remove code = %d, want 204", rr.Code)
	}
}
