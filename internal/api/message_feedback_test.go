package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// message_feedback.go — typed feedback signal API for ADLC phase-7.
//
// These tests pin the contract clients (chat UI, embedded widgets, eval
// dataset builder) depend on:
//
//   - 401 on missing user before any DB work (anonymous probes never
//     leak workspace structure).
//   - 400 on unknown signal value, with the allowed list visible in the
//     error message — caller sees what went wrong without a doc lookup.
//   - Idempotent re-submit at the (message_id, user_id, signal) tuple:
//     a network-flake retry replaces reason text instead of multiplying
//     rows.
//   - Workspace scoping on List so a user with multiple memberships sees
//     feedback across all of them, but never feedback from workspaces
//     they have no membership in.
// ---------------------------------------------------------------------------

func newFeedbackTestHandler(t *testing.T) *MessageFeedbackHandler {
	t.Helper()
	db := setupTestDB(t)
	return NewMessageFeedbackHandler(db, newTestLogger())
}

type feedbackTestBed struct {
	h         *MessageFeedbackHandler
	userID    string
	otherID   string
	wsID      string
	chatID    string
	messageID string
}

func setupFeedbackTestBed(t *testing.T) *feedbackTestBed {
	t.Helper()
	h := newFeedbackTestHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	seedCrewRow(t, h.db, "crew-fb", wsID, "C", "c-fb")
	seedAgentRow(t, h.db, "agent-fb", wsID, "crew-fb", "A", "a-fb", "AGENT")

	chatID := "chat-fb"
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
        VALUES (?, 'agent-fb', ?, ?, 'fb')`, chatID, wsID, userID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	otherID := "user-other-fb"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@fb.com', 'Other')`, otherID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	otherWS := "ws-other-fb"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o-fb')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
        VALUES ('m-fb-other', ?, ?, 'OWNER')`, otherWS, otherID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	messageID := "msg-fb-1"
	seedConvMessage(t, h.db, messageID, chatID, "agent-fb")

	return &feedbackTestBed{
		h:         h,
		userID:    userID,
		otherID:   otherID,
		wsID:      wsID,
		chatID:    chatID,
		messageID: messageID,
	}
}

// seedConvMessage inserts a row into the conversation_messages search
// mirror (migration v111) — the table message_feedback.Create checks
// (#1208) to confirm message_id refers to a real message before creating
// a feedback row. Production rows land here via conversation.Store.Append;
// tests insert directly since there's no conversation store wired up in
// the API-handler test harness.
func seedConvMessage(t *testing.T, db interface {
	Exec(query string, args ...any) (sql.Result, error)
}, messageID, sessionID, agentID string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO conversation_messages (id, session_id, agent_id, role, content)
        VALUES (?, ?, ?, 'user', 'test message')`, messageID, sessionID, agentID); err != nil {
		t.Fatalf("seed conversation_messages row %q: %v", messageID, err)
	}
}

func feedbackReq(method, url, body, userID string) *http.Request {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	}
	return req
}

// ---- Create ----

func TestFeedback_Create_NoAuth_401(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"x","signal":"helpful"}`, "")
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create = %d, want 401 (must come before DB work)", rr.Code)
	}
}

func TestFeedback_Create_UnknownSignal_400(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+bed.messageID+`","chat_id":"`+bed.chatID+`","signal":"super_unhelpful"}`,
		bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("unknown signal = %d, want 400", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "helpful") {
		t.Errorf("error body should list allowed signals; got %s", rr.Body.String())
	}
}

func TestFeedback_Create_MissingMessageID_400(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"signal":"helpful"}`, bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing message_id = %d, want 400", rr.Code)
	}
}

func TestFeedback_Create_CrossWorkspaceChat_404(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// otherID is not a member of bed.wsID — submitting feedback against
	// a chat in that workspace must return 404 (no existence leak).
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+bed.messageID+`","chat_id":"`+bed.chatID+`","signal":"helpful"}`,
		bed.otherID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-ws create = %d, want 404", rr.Code)
	}
}

// TestFeedback_Create_UnknownMessage_404 pins issue #1208: message_id must
// reference a real message before a feedback row is created. Before this
// fix, `feedback create --message msg_doesnotexist --signal helpful`
// silently succeeded (201) and left an orphan row nothing could trace back
// to a real message.
func TestFeedback_Create_UnknownMessage_404(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"msg_doesnotexist12345","chat_id":"`+bed.chatID+`","signal":"helpful"}`,
		bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("create with unknown message_id = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "message not found") {
		t.Errorf("error body = %q, want to mention 'message not found'", rr.Body.String())
	}
	// No orphan row must have been created.
	var count int
	if err := bed.h.db.QueryRow(
		`SELECT COUNT(*) FROM message_feedback WHERE message_id = 'msg_doesnotexist12345'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("orphan feedback row created for nonexistent message: count=%d", count)
	}
}

func TestFeedback_Create_HappyPath_201(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+bed.messageID+`","chat_id":"`+bed.chatID+
			`","signal":"not_helpful","reason":"hallucinated a tool","trace_id":"tr-abc"}`,
		bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID == "" {
		t.Error("expected non-empty id in response")
	}
	// Confirm row landed in DB with the trace_id linked.
	var traceID string
	if err := bed.h.db.QueryRow(
		`SELECT trace_id FROM message_feedback WHERE id = ?`, body.ID).
		Scan(&traceID); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if traceID != "tr-abc" {
		t.Errorf("stored trace_id = %q, want tr-abc", traceID)
	}
}

// TestFeedback_Create_Idempotent verifies the (message_id, user_id, signal)
// UPSERT contract: re-submitting the same triple replaces the reason
// text but keeps a single row. Without this, a UI that retries on
// network flakiness would create duplicate signal rows that skew rolling
// averages by counting "one user changed their mind" as N votes.
func TestFeedback_Create_Idempotent(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	post := func(reason string) string {
		req := feedbackReq("POST", "/api/v1/feedback",
			`{"message_id":"`+bed.messageID+`","chat_id":"`+bed.chatID+
				`","signal":"not_helpful","reason":"`+reason+`"}`, bed.userID)
		rr := httptest.NewRecorder()
		bed.h.Create(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create = %d body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode create response: %v body=%s", err, rr.Body.String())
		}
		return body.ID
	}
	id1 := post("first reason")
	id2 := post("second reason")
	if id1 != id2 {
		t.Errorf("idempotent upsert returned different ids: %s != %s", id1, id2)
	}
	var count int
	if err := bed.h.db.QueryRow(
		`SELECT COUNT(*) FROM message_feedback WHERE message_id = ? AND user_id = ? AND signal = 'not_helpful'`,
		bed.messageID, bed.userID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count after duplicate post = %d, want 1", count)
	}
	var reason string
	if err := bed.h.db.QueryRow(
		`SELECT reason FROM message_feedback WHERE id = ?`, id1).Scan(&reason); err != nil {
		t.Fatalf("reason lookup: %v", err)
	}
	if reason != "second reason" {
		t.Errorf("reason after upsert = %q, want %q", reason, "second reason")
	}
}

// TestFeedback_Create_OversizeBody_413 covers the BEFORE-parse cap
// (MaxBytesReader). A body larger than maxFeedbackBodyBytes must
// produce 413 without ever reaching the field-level checks — that's
// what stops a malicious client from forcing Decode to allocate
// multi-MB strings before our trim/length validation fires.
func TestFeedback_Create_OversizeBody_413(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// Build a >16 KiB payload by stuffing a huge reason. The 4096-char
	// field-level cap would catch this at the validation layer, but
	// we're testing that MaxBytesReader catches it FIRST, before
	// allocating the full string.
	huge := strings.Repeat("a", 20*1024)
	jsonBody, _ := json.Marshal(map[string]string{
		"message_id": bed.messageID,
		"signal":     "edit",
		"reason":     huge,
	})
	req := feedbackReq("POST", "/api/v1/feedback", string(jsonBody), bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize body = %d, want 413", rr.Code)
	}
}

// TestFeedback_Create_OversizeReason_400 caps the free-form reason at
// maxFeedbackReasonChars. A 10 KB payload from a buggy or malicious
// client should be rejected at the API layer rather than pumped into
// the row store.
func TestFeedback_Create_OversizeReason_400(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	oversize := strings.Repeat("a", maxFeedbackReasonChars+10)
	jsonBody, _ := json.Marshal(map[string]string{
		"message_id": bed.messageID,
		"chat_id":    bed.chatID,
		"signal":     "edit",
		"reason":     oversize,
	})
	req := feedbackReq("POST", "/api/v1/feedback", string(jsonBody), bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("oversize reason = %d, want 400", rr.Code)
	}
}

// ---- List ----

func TestFeedback_List_ByTraceID_ScopedToCallerAndWorkspace(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// Seed the caller's own row in bed.wsID with trace tr-1.
	if _, err := bed.h.db.Exec(
		`INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id)
        VALUES ('fb1', ?, ?, 'm1', 'tr-1', 'helpful', '', ?)`,
		bed.wsID, bed.chatID, bed.userID); err != nil {
		t.Fatalf("seed fb1: %v", err)
	}
	// Seed an unrelated row in the OTHER workspace, same trace id. List
	// from bed.userID must not see this — workspace scoping is the
	// belt-and-suspenders gate against cross-tenant probes.
	if _, err := bed.h.db.Exec(
		`INSERT INTO message_feedback (id, workspace_id, message_id, trace_id, signal, reason, user_id)
        VALUES ('fb2', 'ws-other-fb', 'm2', 'tr-1', 'helpful', '', ?)`,
		bed.otherID); err != nil {
		t.Fatalf("seed fb2: %v", err)
	}

	req := feedbackReq("GET", "/api/v1/feedback?trace_id=tr-1", "", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Feedback []feedbackRow `json:"feedback"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Feedback) != 1 {
		t.Fatalf("got %d rows, want 1 (cross-ws leak?)", len(body.Feedback))
	}
	if body.Feedback[0].ID != "fb1" {
		t.Errorf("got id = %s, want fb1", body.Feedback[0].ID)
	}
}

// TestFeedback_List_DoesNotLeakOtherUsersFeedback pins the privacy
// fix: even within the same workspace and on the same message_id, a
// user must NOT be able to read another member's feedback rows. The
// previous List query scoped only by workspace membership, which let
// any workspace member enumerate everyone else's thumbs-downs / "edit"
// reasons by polling the API.
func TestFeedback_List_DoesNotLeakOtherUsersFeedback(t *testing.T) {
	bed := setupFeedbackTestBed(t)

	// Put bed.otherID in bed.wsID as well — they're now both members of
	// the same workspace, mimicking the realistic threat scenario.
	if _, err := bed.h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
        VALUES ('m-other-in-shared', ?, ?, 'MEMBER')`, bed.wsID, bed.otherID); err != nil {
		t.Fatalf("seed shared membership: %v", err)
	}
	// Seed feedback rows for both users against the same message.
	if _, err := bed.h.db.Exec(
		`INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, signal, reason, user_id)
        VALUES ('fb-mine', ?, ?, ?, 'helpful', 'mine', ?), ('fb-other', ?, ?, ?, 'not_helpful', 'other-private', ?)`,
		bed.wsID, bed.chatID, bed.messageID, bed.userID,
		bed.wsID, bed.chatID, bed.messageID, bed.otherID); err != nil {
		t.Fatalf("seed feedback rows: %v", err)
	}

	req := feedbackReq("GET", "/api/v1/feedback?message_id="+bed.messageID, "", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list = %d body=%s", rr.Code, rr.Body.String())
	}
	var body struct {
		Feedback []feedbackRow `json:"feedback"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Feedback) != 1 {
		t.Fatalf("got %d rows, want 1 (other user's feedback leaked?)", len(body.Feedback))
	}
	if body.Feedback[0].ID != "fb-mine" {
		t.Errorf("got id = %s, want fb-mine — privacy gate failed", body.Feedback[0].ID)
	}
	// Belt-and-suspenders: explicitly assert other user's reason text
	// is nowhere in the response body, so a refactor to "List all rows
	// from my workspaces" fails loudly here too.
	if strings.Contains(rr.Body.String(), "other-private") {
		t.Errorf("response leaked another user's reason text: %s", rr.Body.String())
	}
}

func TestFeedback_List_MissingQuery_400(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("GET", "/api/v1/feedback", "", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no query = %d, want 400", rr.Code)
	}
}

// ---- Delete ----

// TestFeedback_Delete_RemovesOwnRow is the under-undo path: a thumb
// toggled off must actually remove the server row so the eval pipeline
// stops counting it. Other users' rows on the same message stay.
func TestFeedback_Delete_RemovesOwnRow(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// Seed the user's row + a row for another user (different signal,
	// shouldn't matter — we filter by user_id AND signal so this is
	// just defensive).
	if _, err := bed.h.db.Exec(
		`INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, signal, user_id)
        VALUES ('fb-own', ?, ?, ?, 'not_helpful', ?), ('fb-other', ?, ?, ?, 'helpful', ?)`,
		bed.wsID, bed.chatID, bed.messageID, bed.userID,
		bed.wsID, bed.chatID, bed.messageID, bed.otherID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := feedbackReq("DELETE",
		"/api/v1/feedback?message_id="+bed.messageID+"&signal=not_helpful",
		"", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete = %d body=%s", rr.Code, rr.Body.String())
	}

	// Own row gone, other user's row preserved.
	var ownCount, otherCount int
	if err := bed.h.db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE id = 'fb-own'`).Scan(&ownCount); err != nil {
		t.Fatalf("count own: %v", err)
	}
	if err := bed.h.db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE id = 'fb-other'`).Scan(&otherCount); err != nil {
		t.Fatalf("count other: %v", err)
	}
	if ownCount != 0 {
		t.Errorf("own row not deleted: count=%d", ownCount)
	}
	if otherCount != 1 {
		t.Errorf("other user's row clobbered: count=%d", otherCount)
	}
}

// TestFeedback_Delete_NonExistent_204 pins the idempotent contract:
// DELETE against a row that doesn't exist returns 204, so a client
// can fire DELETE on every toggle-off click without first checking
// existence.
func TestFeedback_Delete_NonExistent_204(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("DELETE",
		"/api/v1/feedback?message_id=nope&signal=helpful", "", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("delete of non-existent = %d, want 204", rr.Code)
	}
}

func TestFeedback_Delete_NoAuth_401(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("DELETE",
		"/api/v1/feedback?message_id="+bed.messageID+"&signal=helpful", "", "")
	rr := httptest.NewRecorder()
	bed.h.Delete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous delete = %d, want 401", rr.Code)
	}
}

// TestFeedback_Create_UpsertReanchorsWorkspace pins the UPSERT
// workspace_id overwrite: a user with multi-workspace membership who
// first POSTs without chat_id (lands in fallback workspace) and then
// POSTs with chat_id (real workspace) must end up with the row
// pointing at the real workspace. The previous SET clause skipped
// workspace_id and left feedback orphaned in the wrong tenant.
// TestFeedback_Create_WorkspaceAlwaysDerivedFromMessage replaces the old
// TestFeedback_Create_UpsertReanchorsWorkspace, which exercised a
// chat_id-absent "fall back to the caller's own most recent workspace
// membership" path — that path was the cross-tenant existence-probe hole a
// CodeRabbit security finding caught on this PR: workspace/chat is now
// ALWAYS derived from the message's real session_id (conversation_messages),
// never from what the caller happens to belong to or claims in chat_id.
// Confirms that property holds whether chat_id is omitted or explicitly
// (and correctly) supplied — both must resolve to the SAME real workspace —
// and that the UPSERT's workspace_id/chat_id re-anchor still fires cleanly
// across two POSTs of the same (message_id, user_id, signal).
func TestFeedback_Create_WorkspaceAlwaysDerivedFromMessage(t *testing.T) {
	bed := setupFeedbackTestBed(t)

	// Add the user to a second workspace. If any code path still fell back
	// to "most recent membership" this would be picked (created later) —
	// its ABSENCE from the resulting rows below is the regression guard.
	otherWS := "ws-other-membership"
	if _, err := bed.h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := bed.h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at)
        VALUES ('m-other', ?, ?, 'OWNER', datetime('now', '+1 day'))`, otherWS, bed.userID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	// First POST: chat_id omitted — must still resolve to the message's
	// real workspace (bed.wsID), not otherWS and not a 403.
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+bed.messageID+`","signal":"helpful"}`, bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create = %d body=%s", rr.Code, rr.Body.String())
	}

	var firstWS, firstChat string
	if err := bed.h.db.QueryRow(`SELECT workspace_id, COALESCE(chat_id, '') FROM message_feedback
        WHERE message_id = ? AND user_id = ? AND signal = 'helpful'`,
		bed.messageID, bed.userID).Scan(&firstWS, &firstChat); err != nil {
		t.Fatalf("first workspace lookup: %v", err)
	}
	if firstWS != bed.wsID {
		t.Fatalf("first POST (no chat_id) workspace = %q, want %q (the message's real workspace, not otherWS=%q)", firstWS, bed.wsID, otherWS)
	}
	if firstChat != bed.chatID {
		t.Fatalf("first POST (no chat_id) chat = %q, want %q (derived from the message, not left null)", firstChat, bed.chatID)
	}

	// Second POST: chat_id explicitly supplied (and correct) — must
	// resolve to the identical workspace, and the UPSERT must re-anchor
	// cleanly (no duplicate row, no stale value left over).
	req = feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+bed.messageID+`","chat_id":"`+bed.chatID+`","signal":"helpful"}`,
		bed.userID)
	rr = httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("second create = %d body=%s", rr.Code, rr.Body.String())
	}

	var secondWS string
	if err := bed.h.db.QueryRow(`SELECT workspace_id FROM message_feedback
        WHERE message_id = ? AND user_id = ? AND signal = 'helpful'`,
		bed.messageID, bed.userID).Scan(&secondWS); err != nil {
		t.Fatalf("second workspace lookup: %v", err)
	}
	if secondWS != bed.wsID {
		t.Errorf("after re-POST, workspace = %q, want %q — UPSERT didn't re-anchor cleanly", secondWS, bed.wsID)
	}
}

// TestFeedback_Create_CrossTenantChatWithForeignMessage_404 is the direct
// regression test for the CodeRabbit-flagged security finding: a caller
// with legitimate access to their OWN chat must not be able to learn
// whether an unrelated message_id from a DIFFERENT workspace exists by
// submitting their own chat_id alongside it. Before the fix, the existence
// check ran unscoped against conversation_messages regardless of chat_id,
// so this returned 201 (feedback attached to a cross-tenant message) for a
// real foreign message_id, and 404 only for a truly nonexistent one —
// letting an attacker enumerate arbitrary message ids across tenants purely
// from the response code. After the fix, workspace/chat is derived from the
// message's own real session_id, so a foreign message resolves to a chat
// the caller can't see and 404s identically to a nonexistent one.
func TestFeedback_Create_CrossTenantChatWithForeignMessage_404(t *testing.T) {
	bed := setupFeedbackTestBed(t)

	// A second, fully separate tenant: its own workspace, chat, and
	// message — the caller (bed.userID) has no membership here at all.
	foreignWS, foreignChat, foreignMsg := "ws-foreign", "chat-foreign", "msg-foreign"
	if _, err := bed.h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign')`, foreignWS); err != nil {
		t.Fatalf("seed foreign ws: %v", err)
	}
	foreignUser := "user-foreign-fb"
	if _, err := bed.h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign@fb.com', 'Foreign')`, foreignUser); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	seedCrewRow(t, bed.h.db, "crew-foreign", foreignWS, "CF", "c-foreign")
	seedAgentRow(t, bed.h.db, "agent-foreign", foreignWS, "crew-foreign", "AF", "a-foreign", "AGENT")
	if _, err := bed.h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
        VALUES (?, 'agent-foreign', ?, ?, 'foreign')`, foreignChat, foreignWS, foreignUser); err != nil {
		t.Fatalf("seed foreign chat: %v", err)
	}
	seedConvMessage(t, bed.h.db, foreignMsg, foreignChat, "agent-foreign")

	// Attacker submits THEIR OWN valid chat_id (passes any naive
	// chat-visibility check) alongside the foreign message_id.
	req := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"`+foreignMsg+`","chat_id":"`+bed.chatID+`","signal":"helpful"}`,
		bed.userID)
	rr := httptest.NewRecorder()
	bed.h.Create(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant probe with own chat_id + foreign message_id = %d, want 404 (got body=%s)", rr.Code, rr.Body.String())
	}

	// Same response shape as a message_id that doesn't exist anywhere —
	// the two cases must not be distinguishable from the response alone.
	req2 := feedbackReq("POST", "/api/v1/feedback",
		`{"message_id":"msg-does-not-exist-anywhere","chat_id":"`+bed.chatID+`","signal":"helpful"}`,
		bed.userID)
	rr2 := httptest.NewRecorder()
	bed.h.Create(rr2, req2)
	if rr2.Code != rr.Code || rr2.Body.String() != rr.Body.String() {
		t.Errorf("cross-tenant-exists response (%d %q) differs from genuinely-missing response (%d %q) — existence is distinguishable, oracle not fully closed",
			rr.Code, rr.Body.String(), rr2.Code, rr2.Body.String())
	}

	// No feedback row was created for the foreign message under any
	// workspace — the attempted attach must not have partially succeeded.
	var count int
	if err := bed.h.db.QueryRow(`SELECT COUNT(*) FROM message_feedback WHERE message_id = ?`, foreignMsg).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("foreign message has %d feedback row(s), want 0 — the cross-tenant attach must have been fully rejected", count)
	}
}
