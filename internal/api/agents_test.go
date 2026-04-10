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
			name:       "COORDINATOR with crew_id returns 400",
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

func TestParseListPagination(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		defaultLimit int
		maxLimit     int
		wantLimit    int
		wantOffset   int
	}{
		{"unspecified uses defaults", "", 100, 500, 100, 0},
		{"valid values passed through", "?limit=25&offset=50", 100, 500, 25, 50},
		{"limit above max is clamped", "?limit=9999", 100, 500, 500, 0},
		{"limit exactly at max", "?limit=500", 100, 500, 500, 0},
		{"zero limit falls back to default", "?limit=0", 100, 500, 100, 0},
		{"negative limit falls back to default", "?limit=-5", 100, 500, 100, 0},
		{"non-numeric limit falls back to default", "?limit=abc", 100, 500, 100, 0},
		{"negative offset clamped to zero", "?offset=-10", 100, 500, 100, 0},
		{"both clamped", "?limit=99999&offset=-1", 50, 200, 200, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x"+tc.query, nil)
			gotLimit, gotOffset := parseListPagination(req, tc.defaultLimit, tc.maxLimit)
			if gotLimit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", gotLimit, tc.wantLimit)
			}
			if gotOffset != tc.wantOffset {
				t.Errorf("offset = %d, want %d", gotOffset, tc.wantOffset)
			}
		})
	}
}

// TestBatchCountByAgentID covers the helper that replaced the scalar COUNT
// subqueries in the agent-list handler. Verifies: correct grouping, absent
// agent IDs, empty input, and the IN-clause placeholder construction.
func TestBatchCountByAgentID(t *testing.T) {
	db := setupTestDB(t)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr', ?, 'C', 'c')`, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	_, err = db.Exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES
		('ag1', 'cr', ?, 'a1', 'a1'),
		('ag2', 'cr', ?, 'a2', 'a2'),
		('ag3', 'cr', ?, 'a3', 'a3')`, wsID, wsID, wsID)
	if err != nil {
		t.Fatalf("insert agents: %v", err)
	}
	// ag1 has 2 chats, ag2 has 1, ag3 has 0.
	_, err = db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES
		('c1', 'ag1', ?, 'CHAT', 'ACTIVE'),
		('c2', 'ag1', ?, 'CHAT', 'ACTIVE'),
		('c3', 'ag2', ?, 'CHAT', 'ACTIVE')`, wsID, wsID, wsID)
	if err != nil {
		t.Fatalf("insert chats: %v", err)
	}

	counts, err := batchCountByAgentID(
		req(t).Context(),
		db,
		`SELECT agent_id, COUNT(*) FROM chats WHERE agent_id IN (%s) GROUP BY agent_id`,
		[]string{"ag1", "ag2", "ag3", "missing"},
	)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	// ag3 has zero chats -> GROUP BY omits it entirely; caller must treat
	// missing-from-map as zero. "missing" agent is similarly absent.
	if counts["ag1"] != 2 {
		t.Errorf("ag1 count = %d, want 2", counts["ag1"])
	}
	if counts["ag2"] != 1 {
		t.Errorf("ag2 count = %d, want 1", counts["ag2"])
	}
	if _, ok := counts["ag3"]; ok {
		t.Errorf("ag3 should be absent from result (zero chats); got %v", counts["ag3"])
	}
	if _, ok := counts["missing"]; ok {
		t.Error("missing agent should not appear in result")
	}

	// Empty input must short-circuit without touching the DB.
	empty, err := batchCountByAgentID(
		req(t).Context(),
		db,
		`SELECT agent_id, COUNT(*) FROM chats WHERE agent_id IN (%s) GROUP BY agent_id`,
		nil,
	)
	if err != nil {
		t.Fatalf("empty input: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty input returned %d entries", len(empty))
	}
}

// req is a tiny helper to get a context-bearing http.Request in the few tests
// that need one but don't care about routing.
func req(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest("GET", "/x", nil)
}
