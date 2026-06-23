package api

// Coverage tests for mission_handler.go (List/ListAll/Start/Metrics) plus a
// few remaining mutate branches in mission_handler_mutate.go. Reuses
// seedMissionCrew / seedMissionAgent from missions_test.go and
// covMMSeedMission from mission_handler_mutate_cov_test.go.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type covMHRig struct {
	h      *MissionHandler
	db     *sql.DB
	userID string
	wsID   string
	crewID string
	leadID string
}

func newCovMHRig(t *testing.T) *covMHRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedMissionCrew(t, db, wsID)
	leadID := seedMissionAgent(t, db, wsID, crewID, "lead-mh", "LEAD")
	return &covMHRig{
		h:  NewMissionHandler(db, nil, nil, covMMLogger()),
		db: db, userID: userID, wsID: wsID, crewID: crewID, leadID: leadID,
	}
}

func (r *covMHRig) seedTask(t *testing.T, id, missionID, status string, order int) {
	t.Helper()
	if _, err := r.db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, created_at, updated_at)
		VALUES (?, ?, ?, 'Task', ?, ?, '[]', datetime('now'), datetime('now'))`,
		id, missionID, r.leadID, status, order); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

func (r *covMHRig) get(target, crewID, missionID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if crewID != "" {
		req.SetPathValue("crewId", crewID)
	}
	if missionID != "" {
		req.SetPathValue("missionId", missionID)
	}
	return withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
}

// --- List ----------------------------------------------------------------

func TestCovMHList_EmptyAndStatusFilter(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-pln", r.wsID, r.crewID, r.leadID, "PLANNING")
	covMMSeedMission(t, r.db, "m-done", r.wsID, r.crewID, r.leadID, "COMPLETED")
	r.seedTask(t, "t1", "m-pln", "COMPLETED", 1)
	r.seedTask(t, "t2", "m-pln", "PENDING", 2)

	t.Run("status filter", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.List(rec, r.get("/x?status=PLANNING", r.crewID, ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var out []missionResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(out) != 1 || out[0].ID != "m-pln" {
			t.Fatalf("filtered list = %+v, want only m-pln", out)
		}
		if out[0].TaskStats == nil || out[0].TaskStats.Total != 2 {
			t.Errorf("task stats = %+v, want total 2", out[0].TaskStats)
		}
	})

	t.Run("empty list serializes as []", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.List(rec, r.get("/x", "no-such-crew", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("body = %q, want []", rec.Body.String())
		}
	})
}

func TestCovMHList_DBError500(t *testing.T) {
	r := newCovMHRig(t)
	r.db.Close()
	rec := httptest.NewRecorder()
	r.h.List(rec, r.get("/x", r.crewID, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- ListAll ----------------------------------------------------------------

func TestCovMHListAll_IncludeTasksAndStatusFilter(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "ma-1", r.wsID, r.crewID, r.leadID, "PLANNING")
	r.seedTask(t, "ta-1", "ma-1", "PENDING", 1)

	rec := httptest.NewRecorder()
	r.h.ListAll(rec, r.get("/api/v1/missions?include_tasks=true&status=PLANNING", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var out []missionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if len(out[0].Tasks) != 1 || out[0].Tasks[0].ID != "ta-1" {
		t.Errorf("tasks = %+v, want ta-1 included", out[0].Tasks)
	}
	if out[0].TaskStats == nil || out[0].TaskStats.Total != 1 {
		t.Errorf("task stats = %+v", out[0].TaskStats)
	}
}

func TestCovMHListAll_EmptyAndDBError(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		r := newCovMHRig(t)
		rec := httptest.NewRecorder()
		r.h.ListAll(rec, r.get("/api/v1/missions", "", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("body = %q, want []", rec.Body.String())
		}
	})
	t.Run("db error", func(t *testing.T) {
		r := newCovMHRig(t)
		r.db.Close()
		rec := httptest.NewRecorder()
		r.h.ListAll(rec, r.get("/api/v1/missions", "", ""))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// --- Start ----------------------------------------------------------------

func TestCovMHStart(t *testing.T) {
	t.Run("not found 404", func(t *testing.T) {
		r := newCovMHRig(t)
		rec := httptest.NewRecorder()
		req := r.get("/x", r.crewID, "ghost")
		req.Method = http.MethodPost
		r.h.Start(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("non-PLANNING 400", func(t *testing.T) {
		r := newCovMHRig(t)
		covMMSeedMission(t, r.db, "m-run", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
		rec := httptest.NewRecorder()
		r.h.Start(rec, r.get("/x", r.crewID, "m-run"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("success flips status", func(t *testing.T) {
		r := newCovMHRig(t)
		covMMSeedMission(t, r.db, "m-go", r.wsID, r.crewID, r.leadID, "PLANNING")
		rec := httptest.NewRecorder()
		r.h.Start(rec, r.get("/x", r.crewID, "m-go"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var status string
		if err := r.db.QueryRow(`SELECT status FROM missions WHERE id = 'm-go'`).Scan(&status); err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "IN_PROGRESS" {
			t.Errorf("status = %q, want IN_PROGRESS", status)
		}
	})

	t.Run("db error 500", func(t *testing.T) {
		r := newCovMHRig(t)
		r.db.Close()
		rec := httptest.NewRecorder()
		r.h.Start(rec, r.get("/x", r.crewID, "m-x"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// --- Metrics ----------------------------------------------------------------

func TestCovMHMetrics_PopulatedWorkspace(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "mm-active", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	covMMSeedMission(t, r.db, "mm-done", r.wsID, r.crewID, r.leadID, "COMPLETED")
	if _, err := r.db.Exec(`UPDATE missions SET completed_at = datetime('now') WHERE id = 'mm-done'`); err != nil {
		t.Fatalf("update: %v", err)
	}
	r.seedTask(t, "mt-c", "mm-done", "COMPLETED", 1)
	if _, err := r.db.Exec(`UPDATE mission_tasks SET completed_at = datetime('now'), tokens_used = 100, estimated_cost = 0.5 WHERE id = 'mt-c'`); err != nil {
		t.Fatalf("update task: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.Metrics(rec, r.get("/api/v1/mission-metrics", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var m struct {
		TotalMissions  int `json:"total_missions"`
		ActiveMissions int `json:"active_missions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.TotalMissions != 2 || m.ActiveMissions != 1 {
		t.Errorf("totals = %+v, want total 2 / active 1", m)
	}
}

func TestCovMHMetrics_DBError500(t *testing.T) {
	r := newCovMHRig(t)
	r.db.Close()
	rec := httptest.NewRecorder()
	r.h.Metrics(rec, r.get("/x", "", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- mutate: remaining branches -------------------------------------------

func TestCovMHMutate_CreateTitleMissing400(t *testing.T) {
	r := newCovMHRig(t)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"lead_agent_id":"lead-mh"}`))
	req.SetPathValue("crewId", r.crewID)
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCovMHMutate_CreateLeadLookupError500(t *testing.T) {
	r := newCovMHRig(t)
	r.db.Close()
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"title":"T","lead_agent_id":"lead-mh"}`))
	req.SetPathValue("crewId", r.crewID)
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovMHMutate_DeleteWrongStatus400(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-del-run", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("crewId", r.crewID)
	req.SetPathValue("missionId", "m-del-run")
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE id = 'm-del-run'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("mission should survive (n=%d err=%v)", n, err)
	}
}

func TestCovMHMutate_CreateLeadIDMissing400(t *testing.T) {
	r := newCovMHRig(t)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"title":"T"}`))
	req.SetPathValue("crewId", r.crewID)
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "lead_agent_id") {
		t.Errorf("body = %q, want lead_agent_id error", rec.Body.String())
	}
}

func TestCovMHMutate_DeleteNotFound404(t *testing.T) {
	r := newCovMHRig(t)
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("crewId", r.crewID)
	req.SetPathValue("missionId", "ghost-mission")
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCovMHMutate_DeleteDBError500(t *testing.T) {
	r := newCovMHRig(t)
	r.db.Close()
	req := httptest.NewRequest(http.MethodDelete, "/x", nil)
	req.SetPathValue("crewId", r.crewID)
	req.SetPathValue("missionId", "m-x")
	req = withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
	rec := httptest.NewRecorder()
	r.h.Delete(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
