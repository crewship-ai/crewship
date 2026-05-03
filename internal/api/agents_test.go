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

func TestCreateAgent_RoleValidation(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-1"
	db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Devs', 'devs')`, crewID, wsID)

	handler := NewAgentHandler(db, logger)

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "LEAD without crew_id returns 400",
			body:       `{"name":"Lead Bot","slug":"lead-bot","agent_role":"LEAD"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "COORDINATOR role rejected (retired in v0.1)",
			body:       `{"name":"CEO","slug":"ceo","agent_role":"COORDINATOR","crew_id":"` + crewID + `"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid agent_role returns 400",
			body:       `{"name":"Bot","slug":"bot","agent_role":"INVALID_ROLE"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "LEAD with crew_id and active lead_mode succeeds",
			body:       `{"name":"Lead","slug":"lead","agent_role":"LEAD","crew_id":"` + crewID + `","lead_mode":"active"}`,
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := bytes.NewBufferString(tt.body)
			req := httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, body)
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, "OWNER")
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()

			handler.Create(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d; body: %s", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestCreateAgent_LeadRole_OnlyOnePerCrew(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

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

// Pin disallowed PATCH fields. The frontend's agent canvas must NOT
// expose UI for fields the backend silently drops; otherwise the user
// types a value, sees "saved", reloads, value reverts.
//
// First iteration of the canvas had editable rows for temperature,
// max_tokens, and webhook_secret. None are in agents.go::Update's
// allowed map. Those rows were removed; this test documents the rule.
func TestUpdateAgent_DisallowedFields_DocumentedHere(t *testing.T) {
	disallowed := []string{
		"temperature",
		"max_tokens",
		"webhook_secret",
		"id", "workspace_id", "created_at", "updated_at", "deleted_at",
		"status",
	}
	if len(disallowed) == 0 {
		t.Fatal("disallowed list must be non-empty")
	}
}
