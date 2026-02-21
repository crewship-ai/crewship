package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func seedMissionCrew(t *testing.T, db *sql.DB, wsID string) string {
	t.Helper()
	crewID := "mission-crew-id"
	_, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Mission Crew', 'mission-crew')`, crewID, wsID)
	if err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	return crewID
}

func seedMissionAgent(t *testing.T, db *sql.DB, wsID, crewID, agentID, role string) string {
	t.Helper()
	slug := "agent-" + agentID
	_, err := db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES (?, ?, ?, ?, ?, ?, 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		agentID, wsID, crewID, "Agent "+role, slug, role)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return agentID
}

func TestMissionCreate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"title":"Build Feature X","description":"Implement feature X","lead_agent_id":"` + leadID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["title"] != "Build Feature X" {
		t.Errorf("title = %v, want 'Build Feature X'", result["title"])
	}
	if result["status"] != "PLANNING" {
		t.Errorf("status = %v, want PLANNING", result["status"])
	}
	traceID, ok := result["trace_id"].(string)
	if !ok || len(traceID) < 10 {
		t.Errorf("trace_id = %v, want non-empty string starting with 'mission-'", result["trace_id"])
	}
}

func TestMissionCreate_Forbidden(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"title":"Test","lead_agent_id":"` + leadID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "VIEWER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestMissionCreate_MissingFields(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"description":"no title"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestMissionCreate_LeadRequired(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	agentID := seedMissionAgent(t, db, wsID, crewID, "agent-1", "AGENT")

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"title":"Test","lead_agent_id":"` + agentID + `"}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions", body)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestMissionList(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	// Insert a mission directly
	_, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-test1', 'Mission 1', 'PLANNING', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID)
	if err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions?workspace_id="+wsID, nil)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
	if result[0]["title"] != "Mission 1" {
		t.Errorf("title = %v, want 'Mission 1'", result[0]["title"])
	}
}

func TestMissionList_StatusFilter(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-t1', 'M1', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission m1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m2', ?, ?, ?, 'mission-t2', 'M2', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission m2: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions?status=IN_PROGRESS", nil)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("len = %d, want 1 (filtered)", len(result))
	}
}

func TestMissionGet(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-g1', 'Mission Get', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('mt1', 'm1', 'Task 1', 'PENDING', 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions/m1", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["title"] != "Mission Get" {
		t.Errorf("title = %v, want 'Mission Get'", result["title"])
	}
	tasks, ok := result["tasks"].([]interface{})
	if !ok || len(tasks) != 1 {
		t.Errorf("tasks len = %v, want 1", result["tasks"])
	}
}

func TestMissionGet_NotFound(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/crews/"+crewID+"/missions/nonexistent", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "nonexistent")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Get(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestMissionUpdate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-u1', 'Mission Update', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m1", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["status"] != "IN_PROGRESS" {
		t.Errorf("status = %v, want IN_PROGRESS", result["status"])
	}
}

func TestMissionUpdate_InvalidTransition(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-it1', 'Mission', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m1", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestMissionTaskCreate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-tc1', 'Mission', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"title":"Write tests","task_order":1}`)
	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/missions/m1/tasks", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.CreateTask(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["title"] != "Write tests" {
		t.Errorf("title = %v, want 'Write tests'", result["title"])
	}
	if result["status"] != "PENDING" {
		t.Errorf("status = %v, want PENDING", result["status"])
	}
}

func TestMissionTaskUpdate(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-tu1', 'Mission', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, created_at, updated_at)
		VALUES ('mt1', 'm1', 'Task 1', 'PENDING', 0, datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m1/tasks/mt1", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	req.SetPathValue("taskId", "mt1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdateTask(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["status"] != "IN_PROGRESS" {
		t.Errorf("status = %v, want IN_PROGRESS", result["status"])
	}
}

func TestMissionTaskUpdate_UnblocksDependents(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-ub1', 'Mission', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	// Task 1: IN_PROGRESS (will be completed)
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('mt1', 'm1', 'Task 1', 'IN_PROGRESS', 0, '[]', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert task mt1: %v", err)
	}
	// Task 2: BLOCKED (depends on mt1)
	if _, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES ('mt2', 'm1', 'Task 2', 'BLOCKED', 1, '["mt1"]', datetime('now'), datetime('now'))`); err != nil {
		t.Fatalf("insert task mt2: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	// Complete mt1
	body := bytes.NewBufferString(`{"status":"COMPLETED","result_summary":"Done"}`)
	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/missions/m1/tasks/mt1", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	req.SetPathValue("taskId", "mt1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.UpdateTask(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify mt2 is now PENDING (unblocked)
	var mt2Status string
	err := db.QueryRow("SELECT status FROM mission_tasks WHERE id = 'mt2'").Scan(&mt2Status)
	if err != nil {
		t.Fatalf("query mt2: %v", err)
	}
	if mt2Status != "PENDING" {
		t.Errorf("mt2 status = %q, want PENDING (should be unblocked)", mt2Status)
	}
}

func TestMissionListAll(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-la1', 'Mission 1', 'IN_PROGRESS', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("GET", "/api/v1/missions?workspace_id="+wsID, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MEMBER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.ListAll(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var result []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("len = %d, want 1", len(result))
	}
}

func TestMissionDelete(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-1", "LEAD")

	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('m1', ?, ?, ?, 'mission-d1', 'Mission Del', 'PLANNING', datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	handler := NewMissionHandler(db, nil, logger)

	req := httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/missions/m1", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "m1")
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "MANAGER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d; body: %s", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// Verify deleted
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM missions WHERE id = 'm1'").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 0 {
		t.Errorf("mission still exists after delete")
	}
}
