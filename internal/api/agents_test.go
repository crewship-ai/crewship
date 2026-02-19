package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func seedTestCrew(t *testing.T, db interface{ Exec(string, ...interface{}) (interface{ RowsAffected() (int64, error) }, error) }, wsID string) string {
	t.Helper()
	crewID := "test-crew-id"
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Test Crew', 'test-crew')`, crewID, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	return crewID
}

func TestCreateAgent_LeadRole_RequiresCrewID(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewAgentHandler(db, logger)

	// LEAD without crew_id should fail
	body := bytes.NewBufferString(`{"name":"Lead Bot","slug":"lead-bot","agent_role":"LEAD"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestCreateAgent_LeadRole_OnlyOnePerCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Create crew
	crewID := "crew-1"
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Devs', 'devs')`, crewID, wsID)

	handler := NewAgentHandler(db, logger)

	// Create first lead -- should succeed
	body := bytes.NewBufferString(`{"name":"Lead 1","slug":"lead-1","agent_role":"LEAD","crew_id":"` + crewID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("first lead: status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Create second lead in same crew -- should fail
	body = bytes.NewBufferString(`{"name":"Lead 2","slug":"lead-2","agent_role":"LEAD","crew_id":"` + crewID + `"}`)
	req = httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx = withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr = httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusConflict {
		t.Errorf("second lead: status = %d, want %d; body: %s", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

func TestCreateAgent_CoordinatorRequiresNoCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-1"
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Devs', 'devs')`, crewID, wsID)

	handler := NewAgentHandler(db, logger)

	// COORDINATOR with crew_id should fail
	body := bytes.NewBufferString(`{"name":"CEO","slug":"ceo","agent_role":"COORDINATOR","crew_id":"` + crewID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestCreateAgent_InvalidAgentRole(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	handler := NewAgentHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Bot","slug":"bot","agent_role":"INVALID_ROLE"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestCreateAgent_ValidLeadMode(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-1"
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Devs', 'devs')`, crewID, wsID)

	handler := NewAgentHandler(db, logger)

	body := bytes.NewBufferString(`{"name":"Lead","slug":"lead","agent_role":"LEAD","crew_id":"` + crewID + `","lead_mode":"active"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var agent agentResponse
	json.Unmarshal(rr.Body.Bytes(), &agent)
	if agent.LeadMode == nil || *agent.LeadMode != "active" {
		t.Errorf("lead_mode = %v, want 'active'", agent.LeadMode)
	}
}

func TestUpdateAgent_PromoteToLead_DemotesPrevious(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-1"
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Devs', 'devs')`, crewID, wsID)

	// Create a lead agent directly in DB
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled, lead_mode)
		VALUES ('agent-lead', ?, ?, 'Old Lead', 'old-lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0, 'active')`, wsID, crewID)

	// Create a regular agent
	db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES ('agent-regular', ?, ?, 'Regular', 'regular', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`, wsID, crewID)

	handler := NewAgentHandler(db, logger)

	// Update agent-regular to LEAD
	body := bytes.NewBufferString(`{"agent_role":"LEAD"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/agents/agent-regular?workspace_id="+wsID, body)
	req.SetPathValue("agentId", "agent-regular")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify old lead was demoted
	var oldRole string
	err := db.QueryRow("SELECT agent_role FROM agents WHERE id = 'agent-lead'").Scan(&oldRole)
	if err != nil {
		t.Fatalf("query old lead: %v", err)
	}
	if oldRole != "AGENT" {
		t.Errorf("old lead role = %q, want AGENT", oldRole)
	}

	// Verify new lead was promoted
	var newRole string
	err = db.QueryRow("SELECT agent_role FROM agents WHERE id = 'agent-regular'").Scan(&newRole)
	if err != nil {
		t.Fatalf("query new lead: %v", err)
	}
	if newRole != "LEAD" {
		t.Errorf("new lead role = %q, want LEAD", newRole)
	}
}
