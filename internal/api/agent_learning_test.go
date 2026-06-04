package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// LearningHandler.Get exposes the self-learning flag + audit trail to
// any workspace member; Patch flips it and requires ADMIN+ (canRole
// "manage") with an explicit enabled bool and a non-empty reason.

func TestLearningGet_MissingAgentID(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewLearningHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents//learning", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLearningGet_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewLearningHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/ghost/learning", nil)
	req.SetPathValue("agentId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLearningGet_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-learn", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, db, "a-learn", wsID, crewID, "Lea", "lea", "AGENT")

	h := NewLearningHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/learning", nil)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp learningResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AgentID != agentID {
		t.Errorf("agent_id=%q want %q", resp.AgentID, agentID)
	}
	if resp.Enabled {
		t.Errorf("enabled=true want false by default")
	}
}

func TestLearningPatch_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-learn", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, db, "a-learn", wsID, crewID, "Lea", "lea", "AGENT")

	h := NewLearningHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"enabled":true,"reason":"trial"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID+"/learning", body)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "MANAGER") // below ADMIN
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLearningPatch_MissingEnabled(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-learn", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, db, "a-learn", wsID, crewID, "Lea", "lea", "AGENT")

	h := NewLearningHandler(db, newTestLogger())
	// reason present but enabled omitted — must be rejected, not treated as false.
	body := bytes.NewBufferString(`{"reason":"trim audit noise"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID+"/learning", body)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestLearningPatch_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "c-learn", wsID, "Eng", "eng")
	agentID := seedAgentRow(t, db, "a-learn", wsID, crewID, "Lea", "lea", "AGENT")

	h := NewLearningHandler(db, newTestLogger())
	body := bytes.NewBufferString(`{"enabled":true,"reason":"enabling for pilot crew"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/agents/"+agentID+"/learning", body)
	req.SetPathValue("agentId", agentID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Patch(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp learningResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled {
		t.Errorf("enabled=false want true after flip")
	}

	var dbEnabled int
	if err := db.QueryRow("SELECT self_learning_enabled FROM agents WHERE id = ?", agentID).Scan(&dbEnabled); err != nil {
		t.Fatalf("query flag: %v", err)
	}
	if dbEnabled != 1 {
		t.Errorf("db flag=%d want 1", dbEnabled)
	}
}
