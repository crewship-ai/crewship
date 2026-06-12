package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// agents_query_cov_test.go covers remaining AgentHandler List/Get/Delete
// branches: missing workspace/agent ids, the crew-filter path, batch
// count loading (success + failure), Get not-found / created-by, and the
// Delete gate's DB error. Helpers prefixed covAQ.

func TestCovAQ_List_MissingWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovAQ_List_CrewFilterWithCounts(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covaq-crew', ?, 'C', 'covaq-c')`, wsID)
	seedAgentRow(t, db, "covaq-ag", wsID, "covaq-crew", "Aq", "covaq-aq", "AGENT")
	// Another agent in a different crew must be filtered out.
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covaq-other', ?, 'O', 'covaq-o')`, wsID)
	seedAgentRow(t, db, "covaq-ag2", wsID, "covaq-other", "Other", "covaq-other-ag", "AGENT")
	// Rows that feed the count loaders.
	execOrFatal(t, db, `INSERT INTO skills
		(id, name, slug, display_name, description, vendor, version, category, source, verification,
		 downloads, rating_count, pricing_tier, featured, tags, credential_requirements, content)
		VALUES ('covaq-skill', 'S', 'covaq-s', 'S', 'd', 'v', '1.0.0', 'CODING', 'CUSTOM', 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '[]', 'c')`)
	execOrFatal(t, db, `INSERT INTO agent_skills (id, agent_id, skill_id, enabled) VALUES ('covaq-as', 'covaq-ag', 'covaq-skill', 1)`)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('covaq-chat', 'covaq-ag', ?, 'CHAT', 'ACTIVE')`, wsID)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents?crew_id=covaq-crew", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var agents []agentResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 1 || agents[0].ID != "covaq-ag" {
		t.Fatalf("agents = %+v, want exactly covaq-ag", agents)
	}
	if agents[0].Count.Skills != 1 || agents[0].Count.Chats != 1 || agents[0].Count.Credentials != 0 {
		t.Errorf("counts = %+v, want skills=1 chats=1 creds=0", agents[0].Count)
	}
}

func TestCovAQ_List_BatchCountDBError(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covaq-bc', ?, 'C', 'covaq-bc')`, wsID)
	seedAgentRow(t, db, "covaq-bc-ag", wsID, "covaq-bc", "A", "covaq-bc-a", "AGENT")
	// Main list query succeeds; the skills batch count then fails.
	execOrFatal(t, db, `ALTER TABLE agent_skills RENAME TO covaq_agent_skills_bak`)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovAQ_Get_MissingAgentID(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/", nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovAQ_Get_NotFound(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/missing", nil)
	req.SetPathValue("agentId", "missing")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestCovAQ_Get_CreatedByUserSurfaced(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('covaq-gc', ?, 'C', 'covaq-gc')`, wsID)
	seedAgentRow(t, db, "covaq-gc-ag", wsID, "covaq-gc", "A", "covaq-gc-a", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET created_by_user_id = ? WHERE id = 'covaq-gc-ag'`, userID)

	h := NewAgentHandler(db, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/agents/covaq-gc-ag", nil)
	req.SetPathValue("agentId", "covaq-gc-ag")
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var a agentResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a.CreatedByUserID != userID {
		t.Errorf("created_by_user_id = %q, want %q", a.CreatedByUserID, userID)
	}
}

func TestCovAQ_Delete_GateDBError(t *testing.T) {
	db := setupTestDB(t)
	h := NewAgentHandler(db, newTestLogger())
	db.Close()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-x", nil)
	req.SetPathValue("agentId", "ag-x")
	ctx := withUser(req.Context(), &AuthUser{ID: "u1"})
	ctx = withWorkspace(ctx, "ws-x", "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
