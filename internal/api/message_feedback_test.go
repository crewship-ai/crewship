package api

import (
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

	return &feedbackTestBed{
		h:         h,
		userID:    userID,
		otherID:   otherID,
		wsID:      wsID,
		chatID:    chatID,
		messageID: "msg-fb-1",
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
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
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

func TestFeedback_List_ByTraceID_ScopedToWorkspace(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	// Seed a row in bed.wsID with trace tr-1.
	if _, err := bed.h.db.Exec(
		`INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id)
        VALUES ('fb1', ?, ?, 'm1', 'tr-1', 'helpful', '', ?)`,
		bed.wsID, bed.chatID, bed.userID); err != nil {
		t.Fatalf("seed fb1: %v", err)
	}
	// Seed an unrelated row in the OTHER workspace, same trace id. List
	// from bed.userID must not see this — workspace scoping is the gate.
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

func TestFeedback_List_MissingQuery_400(t *testing.T) {
	bed := setupFeedbackTestBed(t)
	req := feedbackReq("GET", "/api/v1/feedback", "", bed.userID)
	rr := httptest.NewRecorder()
	bed.h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("no query = %d, want 400", rr.Code)
	}
}
