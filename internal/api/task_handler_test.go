package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newMissionHandlerForTasks(t *testing.T) (*MissionHandler, string, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	// Insert chat referenced by missions
	missionID := generateCUID()
	if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES (?, ?, ?, 'MISSION', 'ACTIVE')`,
		missionID, leadID, wsID); err != nil {
		t.Fatalf("insert chat: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'Mission', 'IN_PROGRESS', datetime('now'), datetime('now'))`,
		missionID, wsID, crewID, leadID, "trace-"+missionID); err != nil {
		t.Fatalf("insert mission: %v", err)
	}

	return NewMissionHandler(db, nil, nil, logger), userID, wsID, crewID, leadID, missionID
}

func TestTask_CreateTask_Success(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	body := bytes.NewBufferString(`{"title":"Step 1","description":"first","task_order":1}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "PENDING" {
		t.Errorf("status = %q want PENDING", resp.Status)
	}
}

func TestTask_CreateTask_NoTitle(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_CreateTask_BadJSON(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`bad`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_CreateTask_Forbidden(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"title":"x"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_CreateTask_MissionNotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newMissionHandlerForTasks(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"title":"x"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_CreateTask_BadMissionStatus(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Exec(`UPDATE missions SET status='COMPLETED' WHERE id=?`, missionID)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"title":"x"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_CreateTask_DependencyMissing(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)

	body := bytes.NewBufferString(`{"title":"x","depends_on":["bogus"]}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_CreateTask_BlockedByDependency(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	// Create first task
	t1ID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t1ID, missionID, "Task1", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"title":"depends","depends_on":["` + t1ID + `"]}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateTask(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp missionTaskResponse
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Status != "BLOCKED" {
		t.Errorf("status = %q want BLOCKED", resp.Status)
	}
}

// ── UpdateTask ────────────────────────────────────────────────────────

func TestTask_UpdateTask_StatusTransition(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTask_UpdateTask_InvalidTransition(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"status":"COMPLETED"}`) // PENDING->COMPLETED not allowed
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_UpdateTask_TaskNotFound(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"status":"IN_PROGRESS"}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_UpdateTask_StatusAndDeps_Conflict(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"status":"IN_PROGRESS","depends_on":"[]"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_UpdateTask_EditableFieldsBlocked(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "IN_PROGRESS", 1, "[]")

	body := bytes.NewBufferString(`{"title":"renamed"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_UpdateTask_BadJSON(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", "x")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_UpdateTask_Forbidden(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", "x")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_UpdateTask_DependsOnSelf(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"depends_on":"[\"` + tID + `\"]"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_UpdateTask_DependsOnNotInMission(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"depends_on":"[\"missing\"]"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_UpdateTask_BadDependsOnJSON(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "PENDING", 1, "[]")

	body := bytes.NewBufferString(`{"depends_on":"not-json"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", tID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateTask(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// ── Restart, Resume, Clone (state) ────────────────────────────────────

func TestTask_Restart_Forbidden(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_Restart_NotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_Restart_BadStatus(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	// mission is IN_PROGRESS by default

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestTask_Restart_Success(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Exec(`UPDATE missions SET status='COMPLETED' WHERE id=?`, missionID)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "FAILED", 1, "[]")

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Restart(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTask_Resume_BadStatus(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	// mission IN_PROGRESS, can only resume FAILED

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestTask_Resume_NoFailedTasks(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Exec(`UPDATE missions SET status='FAILED' WHERE id=?`, missionID)
	// No tasks at all

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestTask_Resume_Success(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	h.db.Exec(`UPDATE missions SET status='FAILED' WHERE id=?`, missionID)
	tID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, tID, missionID, "Task", "FAILED", 1, "[]")

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTask_Resume_Forbidden(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Resume(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_Clone(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	t1ID := generateCUID()
	t2ID := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t1ID, missionID, "T1", "PENDING", 1, "[]")
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t2ID, missionID, "T2", "PENDING", 2, `["`+t1ID+`"]`)

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Clone(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTask_Clone_NotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newMissionHandlerForTasks(t)

	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Clone(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestTask_Clone_Forbidden(t *testing.T) {
	h, userID, wsID, crewID, _, missionID := newMissionHandlerForTasks(t)
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Clone(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d", rr.Code)
	}
}

// ── Dependency helpers ─────────────────────────────────────────────────

func TestTask_FindUnblockableTasks(t *testing.T) {
	h, _, _, _, _, missionID := newMissionHandlerForTasks(t)
	t1 := generateCUID()
	t2 := generateCUID()
	t3 := generateCUID()
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t1, missionID, "T1", "COMPLETED", 1, "[]")
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t2, missionID, "T2", "BLOCKED", 2, `["`+t1+`"]`)
	h.db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`, t3, missionID, "T3", "BLOCKED", 3, `["`+t1+`","missing"]`)

	tasks := h.findUnblockableTasks(context.Background(), missionID, "")
	// only t2 is unblockable (its deps all COMPLETED). t3 has missing dep.
	if len(tasks) != 1 || tasks[0].id != t2 {
		t.Errorf("got %v, want [t2]", tasks)
	}

	// filter by completed task
	tasks2 := h.findUnblockableTasks(context.Background(), missionID, t1)
	if len(tasks2) != 1 {
		t.Errorf("got %v want one", tasks2)
	}
}

func TestTask_RemapDependencies(t *testing.T) {
	out := remapDependencies(`["a","b"]`, map[string]string{"a": "x", "b": "y"})
	if out != `["x","y"]` {
		t.Errorf("got %q", out)
	}
	if remapDependencies(`[]`, nil) != `[]` {
		t.Errorf("empty deps must return [] literal")
	}
	if remapDependencies(`bogus`, nil) != `[]` {
		t.Errorf("invalid JSON should fall back to []")
	}
}
