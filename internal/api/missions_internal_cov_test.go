package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// missions_internal_cov_test.go — remaining InternalMissionHandler
// branches: lead-agent validation DB error, task insert rollback,
// the Start UPDATE failure, the Get workspace_id guard, and the Get
// task-load failure. Helpers prefixed covMI.

func covMIFixture(t *testing.T) (*InternalMissionHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covmi-crew", wsID, "Crew", "covmi-crew")
	leadID := seedAgentRow(t, db, "covmi-lead", wsID, crewID, "Lead", "covmi-lead", "LEAD")
	h := NewInternalMissionHandler(db, nil, nil, newTestLogger())
	return h, wsID, crewID, leadID
}

func TestCovMI_Create_ValidateLeadDBError_500(t *testing.T) {
	h, wsID, crewID, leadID := covMIFixture(t)
	h.db.Close()
	req := httptest.NewRequest("POST", "/api/v1/internal/missions", jsonBody(map[string]any{
		"title": "M", "lead_agent_id": leadID, "crew_id": crewID, "workspace_id": wsID,
	}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMI_Create_TaskInsertFailure_500_RollsBackMission(t *testing.T) {
	h, wsID, crewID, leadID := covMIFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covmi_block_task BEFORE INSERT ON mission_tasks
		BEGIN SELECT RAISE(ABORT, 'covmi forced'); END`)
	req := httptest.NewRequest("POST", "/api/v1/internal/missions", jsonBody(map[string]any{
		"title": "M", "lead_agent_id": leadID, "crew_id": crewID, "workspace_id": wsID,
		"tasks": []map[string]any{{"title": "T1", "task_order": 1}},
	}))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND title = 'M'`, wsID).Scan(&n); err != nil || n != 0 {
		t.Errorf("missions = %d err=%v, want rollback to 0", n, err)
	}
}

func TestCovMI_Start_UpdateFailure_500(t *testing.T) {
	h, wsID, crewID, leadID := covMIFixture(t)
	execOrFatal(t, h.db, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('covmi-m1', ?, ?, ?, 'covmi-tr1', 'M', 'PLANNING', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID)
	execOrFatal(t, h.db, `CREATE TRIGGER covmi_block_upd BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT, 'covmi forced'); END`)

	req := httptest.NewRequest("POST",
		"/api/v1/internal/missions/covmi-m1/start?workspace_id="+wsID, nil)
	req.SetPathValue("missionId", "covmi-m1")
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMI_Get_MissingWorkspaceID_400(t *testing.T) {
	h, _, _, _ := covMIFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/internal/missions/covmi-m1", nil)
	req.SetPathValue("missionId", "covmi-m1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovMI_Get_TaskLoadFailure_500(t *testing.T) {
	h, wsID, crewID, leadID := covMIFixture(t)
	execOrFatal(t, h.db, `INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES ('covmi-m2', ?, ?, ?, 'covmi-tr2', 'M', 'PLANNING', datetime('now'), datetime('now'))`,
		wsID, crewID, leadID)
	execOrFatal(t, h.db, `ALTER TABLE mission_tasks RENAME TO mt_broken`)

	req := httptest.NewRequest("GET",
		"/api/v1/internal/missions/covmi-m2?workspace_id="+wsID, nil)
	req.SetPathValue("missionId", "covmi-m2")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
