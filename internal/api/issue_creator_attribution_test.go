package api

// issue_creator_attribution_test.go — creator attribution on issues.
//
// Covers the two create paths (internal/agent + public/human), the creator
// object exposed on issue responses, the agent audit trail on internal
// status updates, and the comment author "system" fallback fix.
//
// Tests decode via ad-hoc structs / raw SQL so they compile against the
// pre-fix code and fail red on behaviour, not on compilation.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// creatorPayload mirrors the created_by object in issue responses.
type creatorPayload struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── 1. Internal (agent) create path persists the creator ───────────────────

func TestInternalIssue_Create_PersistsAgentCreator(t *testing.T) {
	h, wsID, crewID, _, _ := newInternalIssueHandler(t)
	workerID := "agent-worker" // seeded by seedIssueFixtures

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `",
		"title":"Agent authored","author_agent_id":"` + workerID + `",
		"author_chat_id":"chat-123","author_run_id":"run-456"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var authorAgentID, authorChatID, authorRunID, authoredVia sql.NullString
	err := h.db.QueryRow(
		`SELECT author_agent_id, author_chat_id, author_run_id, authored_via FROM missions WHERE id = ?`,
		resp["id"]).Scan(&authorAgentID, &authorChatID, &authorRunID, &authoredVia)
	if err != nil {
		t.Fatalf("read provenance columns: %v", err)
	}
	if authorAgentID.String != workerID {
		t.Errorf("author_agent_id = %q, want %q", authorAgentID.String, workerID)
	}
	if authorChatID.String != "chat-123" {
		t.Errorf("author_chat_id = %q, want chat-123", authorChatID.String)
	}
	if authorRunID.String != "run-456" {
		t.Errorf("author_run_id = %q, want run-456", authorRunID.String)
	}
	if authoredVia.String != "agent_tool_call" {
		t.Errorf("authored_via = %q, want agent_tool_call", authoredVia.String)
	}
}

// ── 2. Public (human) create path persists the creator ─────────────────────

func TestIssueCreate_PersistsUserCreator(t *testing.T) {
	h, db, userID, wsID, crewID := covICHandler(t)

	rr := covICPost(t, h, userID, wsID, crewID, `{"title":"Human authored"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID        string          `json:"id"`
		CreatedBy *creatorPayload `json:"created_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var createdByUserID, authoredVia sql.NullString
	err := db.QueryRow(
		`SELECT created_by_user_id, authored_via FROM missions WHERE id = ?`,
		resp.ID).Scan(&createdByUserID, &authoredVia)
	if err != nil {
		t.Fatalf("read creator columns: %v", err)
	}
	if createdByUserID.String != userID {
		t.Errorf("created_by_user_id = %q, want %q", createdByUserID.String, userID)
	}
	if authoredVia.String != "user_api" {
		t.Errorf("authored_via = %q, want user_api", authoredVia.String)
	}

	if resp.CreatedBy == nil {
		t.Fatalf("response created_by missing: %s", rr.Body.String())
	}
	if resp.CreatedBy.Type != "user" || resp.CreatedBy.ID != userID {
		t.Errorf("created_by = %+v, want type=user id=%s", resp.CreatedBy, userID)
	}
}

// ── 3. Responses expose the creator (detail + list) ────────────────────────

func TestIssueGet_IncludesAgentCreator(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-77", "BACKLOG")
	if _, err := db.Exec(
		`UPDATE missions SET author_agent_id = ?, authored_via = 'agent_tool_call' WHERE id = ?`,
		workerID, missionID); err != nil {
		t.Fatalf("stamp creator: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/issues/ENG-77", nil)
	req.SetPathValue("identifier", "ENG-77")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.GetByIdentifier(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var got struct {
		CreatedBy   *creatorPayload `json:"created_by"`
		AuthoredVia string          `json:"authored_via"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CreatedBy == nil {
		t.Fatalf("created_by missing from detail response: %s", rr.Body.String())
	}
	if got.CreatedBy.Type != "agent" || got.CreatedBy.ID != workerID {
		t.Errorf("created_by = %+v, want type=agent id=%s", got.CreatedBy, workerID)
	}
	if got.CreatedBy.Name != "Worker" {
		t.Errorf("created_by.name = %q, want Worker (resolved agent name)", got.CreatedBy.Name)
	}
	if got.AuthoredVia != "agent_tool_call" {
		t.Errorf("authored_via = %q, want agent_tool_call", got.AuthoredVia)
	}
}

func TestIssueList_IncludesUserCreator(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())

	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-78", "BACKLOG")
	if _, err := db.Exec(
		`UPDATE missions SET created_by_user_id = ?, authored_via = 'user_api' WHERE id = ?`,
		userID, missionID); err != nil {
		t.Fatalf("stamp creator: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/issues", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var got []struct {
		Identifier *string         `json:"identifier"`
		CreatedBy  *creatorPayload `json:"created_by"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1", len(got))
	}
	if got[0].CreatedBy == nil {
		t.Fatalf("created_by missing from list response: %s", rr.Body.String())
	}
	if got[0].CreatedBy.Type != "user" || got[0].CreatedBy.ID != userID {
		t.Errorf("created_by = %+v, want type=user id=%s", got[0].CreatedBy, userID)
	}
	if got[0].CreatedBy.Name != "Test User" {
		t.Errorf("created_by.name = %q, want Test User (resolved user full_name)", got[0].CreatedBy.Name)
	}
}

// Legacy rows (no creator columns stamped) must omit created_by, not error.
func TestIssueGet_LegacyRowOmitsCreator(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-79", "BACKLOG")

	req := httptest.NewRequest("GET", "/api/v1/issues/ENG-79", nil)
	req.SetPathValue("identifier", "ENG-79")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.GetByIdentifier(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw, ok := got["created_by"]; ok && string(raw) != "null" {
		t.Errorf("legacy row must not carry created_by, got %s", raw)
	}
}

// ── 4. Internal UpdateStatus writes an agent audit trail ───────────────────

func TestInternalIssue_UpdateStatus_LogsAgentActivity(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	workerID := "agent-worker"
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO","priority":"high","agent_id":"` + workerID + `"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var actorType, actorID, details string
	err := h.db.QueryRow(
		`SELECT actor_type, actor_id, details FROM mission_activity
		 WHERE mission_id = ? AND action = 'status_changed'`, missionID).
		Scan(&actorType, &actorID, &details)
	if err != nil {
		t.Fatalf("status_changed activity row missing: %v", err)
	}
	if actorType != "agent" || actorID != workerID {
		t.Errorf("status_changed actor = %s/%s, want agent/%s", actorType, actorID, workerID)
	}
	if details != "BACKLOG → TODO" {
		t.Errorf("status_changed details = %q, want %q", details, "BACKLOG → TODO")
	}

	err = h.db.QueryRow(
		`SELECT actor_type, actor_id, details FROM mission_activity
		 WHERE mission_id = ? AND action = 'priority_changed'`, missionID).
		Scan(&actorType, &actorID, &details)
	if err != nil {
		t.Fatalf("priority_changed activity row missing: %v", err)
	}
	if actorType != "agent" || actorID != workerID || details != "high" {
		t.Errorf("priority_changed = %s/%s/%s, want agent/%s/high", actorType, actorID, details, workerID)
	}
}

// ── 5. Comment author fallback: reject, don't misattribute ─────────────────
//
// mission_comments' CHECK only allows author_type IN ('user','agent'), so an
// unattributed comment cannot be stored honestly. Pre-fix it was misfiled as
// an agent literally named "system"; now both internal comment paths reject.

func TestInternalIssue_Comment_NoAgentIDRejected(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	// Via CreateComment.
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","body":"no author supplied"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	// Via UpdateStatus's inline comment — rejected before any mutation.
	body = bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO","comment":"inline no author"}`)
	req = httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr = httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "BACKLOG" {
		t.Errorf("status = %q, want BACKLOG (reject must precede mutation)", status)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Errorf("comments = %d, want 0", n)
	}
}

// Status-only internal updates without agent_id stay allowed and are
// attributed to system in the activity trail (mission_activity's CHECK
// allows 'system').
func TestInternalIssue_UpdateStatus_NoAgentIDSystemActivity(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var actorType, actorID string
	if err := h.db.QueryRow(
		`SELECT actor_type, actor_id FROM mission_activity WHERE mission_id = ? AND action = 'status_changed'`,
		missionID).Scan(&actorType, &actorID); err != nil {
		t.Fatalf("activity row: %v", err)
	}
	if actorType != "system" || actorID != "system" {
		t.Errorf("actor = %s/%s, want system/system", actorType, actorID)
	}
}

func TestInternalIssue_Comment_AgentAttributed(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	workerID := "agent-worker"
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"` + workerID + `","body":"from agent"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var authorType, authorID string
	if err := h.db.QueryRow(
		`SELECT author_type, author_id FROM mission_comments WHERE mission_id = ?`,
		missionID).Scan(&authorType, &authorID); err != nil {
		t.Fatalf("comment row: %v", err)
	}
	if authorType != "agent" || authorID != workerID {
		t.Errorf("comment author = %s/%s, want agent/%s", authorType, authorID, workerID)
	}
}
