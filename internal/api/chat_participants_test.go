package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chat_participants.go — group-chat membership. Same non-standard tenancy gate
// as reactions (workspace derived from the chat). These tests cover: Add
// promotes the chat to a group and seeds the owner, adding a non-workspace user
// is 400, listing as a non-member is 404, and Remove drops a row.

func setupParticipantsTestBed(t *testing.T) (*ChatParticipantsHandler, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	h := NewChatParticipantsHandler(db, newTestLogger())

	ownerID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, ownerID)
	seedCrewRow(t, h.db, "crew-p", wsID, "C", "c-part")
	seedAgentRow(t, h.db, "agent-p", wsID, "crew-p", "A", "a-part", "AGENT")

	chatID := "chat-part"
	if _, err := h.db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
		VALUES (?, 'agent-p', ?, ?, 'p')`, chatID, wsID, ownerID); err != nil {
		t.Fatalf("seed chat: %v", err)
	}

	// A colleague in the SAME workspace — addable as a participant.
	colleagueID := "user-colleague"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'petr@x.com', 'Petr')`, colleagueID); err != nil {
		t.Fatalf("seed colleague: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role)
		VALUES ('m-colleague', ?, ?, 'MEMBER')`, wsID, colleagueID); err != nil {
		t.Fatalf("seed colleague member: %v", err)
	}

	// An outsider in a different workspace — must get 404 on the chat.
	outsiderID := "user-outsider"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'out@x.com', 'Out')`, outsiderID); err != nil {
		t.Fatalf("seed outsider: %v", err)
	}

	return h, ownerID, colleagueID, outsiderID, chatID
}

func partReq(method, url, body, userID string) *http.Request {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	}
	return req
}

func TestParticipants_Add_PromotesGroup_AndSeedsOwner(t *testing.T) {
	h, ownerID, colleagueID, _, chatID := setupParticipantsTestBed(t)

	req := partReq("POST", "/api/v1/chats/"+chatID+"/participants", `{"user_id":"`+colleagueID+`"}`, ownerID)
	req.SetPathValue("chatId", chatID)
	rr := httptest.NewRecorder()
	h.Add(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("add status = %d body=%s", rr.Code, rr.Body.String())
	}

	// chat is now a group
	var vis string
	if err := h.db.QueryRow(`SELECT visibility FROM chats WHERE id = ?`, chatID).Scan(&vis); err != nil {
		t.Fatalf("read visibility: %v", err)
	}
	if vis != "group" {
		t.Errorf("visibility = %q, want group", vis)
	}

	// List returns owner (seeded) + colleague
	lreq := partReq("GET", "/api/v1/chats/"+chatID+"/participants", "", ownerID)
	lreq.SetPathValue("chatId", chatID)
	lrr := httptest.NewRecorder()
	h.List(lrr, lreq)
	if lrr.Code != http.StatusOK {
		t.Fatalf("list status = %d", lrr.Code)
	}
	var body struct {
		Participants []participantRow `json:"participants"`
	}
	if err := json.Unmarshal(lrr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Participants) != 2 {
		t.Fatalf("got %d participants, want 2 (owner + colleague): %+v", len(body.Participants), body.Participants)
	}
}

func TestParticipants_Add_NonWorkspaceUser_400(t *testing.T) {
	h, ownerID, _, outsiderID, chatID := setupParticipantsTestBed(t)
	req := partReq("POST", "/api/v1/chats/"+chatID+"/participants", `{"user_id":"`+outsiderID+`"}`, ownerID)
	req.SetPathValue("chatId", chatID)
	rr := httptest.NewRecorder()
	h.Add(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("add outsider = %d, want 400", rr.Code)
	}
}

func TestParticipants_Add_PlainMemberForbidden_403(t *testing.T) {
	h, _, colleagueID, _, chatID := setupParticipantsTestBed(t)
	// colleagueID is a workspace MEMBER but not the chat creator → may not
	// mutate the roster.
	req := partReq("POST", "/api/v1/chats/"+chatID+"/participants", `{"user_id":"`+colleagueID+`"}`, colleagueID)
	req.SetPathValue("chatId", chatID)
	rr := httptest.NewRecorder()
	h.Add(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("plain member add = %d, want 403", rr.Code)
	}
}

func TestParticipants_List_NotMember_404(t *testing.T) {
	h, _, _, outsiderID, chatID := setupParticipantsTestBed(t)
	req := partReq("GET", "/api/v1/chats/"+chatID+"/participants", "", outsiderID)
	req.SetPathValue("chatId", chatID)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("outsider list = %d, want 404 (no existence leak)", rr.Code)
	}
}

func TestParticipants_Remove_OwnerRejected(t *testing.T) {
	h, ownerID, colleagueID, _, chatID := setupParticipantsTestBed(t)
	// promote to group (seeds owner participant)
	areq := partReq("POST", "/api/v1/chats/"+chatID+"/participants", `{"user_id":"`+colleagueID+`"}`, ownerID)
	areq.SetPathValue("chatId", chatID)
	h.Add(httptest.NewRecorder(), areq)

	dreq := partReq("DELETE", "/api/v1/chats/"+chatID+"/participants/"+ownerID, "", ownerID)
	dreq.SetPathValue("chatId", chatID)
	dreq.SetPathValue("userId", ownerID)
	drr := httptest.NewRecorder()
	h.Remove(drr, dreq)
	if drr.Code != http.StatusBadRequest {
		t.Errorf("removing owner = %d, want 400", drr.Code)
	}
}

func TestParticipants_Remove(t *testing.T) {
	h, ownerID, colleagueID, _, chatID := setupParticipantsTestBed(t)
	// add then remove
	areq := partReq("POST", "/api/v1/chats/"+chatID+"/participants", `{"user_id":"`+colleagueID+`"}`, ownerID)
	areq.SetPathValue("chatId", chatID)
	h.Add(httptest.NewRecorder(), areq)

	dreq := partReq("DELETE", "/api/v1/chats/"+chatID+"/participants/"+colleagueID, "", ownerID)
	dreq.SetPathValue("chatId", chatID)
	dreq.SetPathValue("userId", colleagueID)
	drr := httptest.NewRecorder()
	h.Remove(drr, dreq)
	if drr.Code != http.StatusNoContent {
		t.Fatalf("remove status = %d", drr.Code)
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM chat_participants WHERE chat_id = ? AND user_id = ?`, chatID, colleagueID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("colleague still present after remove (%d rows)", n)
	}
}
