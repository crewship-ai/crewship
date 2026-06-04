package api

// Coverage tests for task_state.go: MissionHandler.Restart, Resume, Clone.
//
// Focus: auth/role 403s, missing mission/task 404s, invalid-state 400/409s,
// and happy paths asserting the resulting DB state. The DAG-engine /
// orchestrator-callback branches of Resume (StartMission, ValidateDAG when
// missionEngine != nil) are skipped — we construct the handler with a nil
// missionEngine so those branches are bypassed, exercising the pure-SQL path.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covTSInsertTask inserts a mission_tasks row with the columns the schema
// requires, mirroring the existing missions_test.go inserts.
func covTSInsertTask(t *testing.T, db *sql.DB, id, missionID, title, status string, order int, dependsOn string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on, iteration, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, datetime('now'), datetime('now'))`,
		id, missionID, title, status, order, dependsOn)
	if err != nil {
		t.Fatalf("insert task %s: %v", id, err)
	}
}

// covTSSetMissionStatus forces a mission into a given status.
func covTSSetMissionStatus(t *testing.T, db *sql.DB, missionID, status string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE missions SET status = ? WHERE id = ?`, status, missionID); err != nil {
		t.Fatalf("set mission %s status %s: %v", missionID, status, err)
	}
}

// covTSTaskStatus reads a single task's status.
func covTSTaskStatus(t *testing.T, db *sql.DB, taskID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = ?`, taskID).Scan(&s); err != nil {
		t.Fatalf("read task %s status: %v", taskID, err)
	}
	return s
}

// covTSMissionStatus reads a single mission's status.
func covTSMissionStatus(t *testing.T, db *sql.DB, missionID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&s); err != nil {
		t.Fatalf("read mission %s status: %v", missionID, err)
	}
	return s
}

// covTSFixture builds a handler + seeded workspace/crew/mission and returns them
// along with the seeded mission ID.
func covTSFixture(t *testing.T) (h *MissionHandler, db *sql.DB, userID, wsID, crewID, missionID string) {
	t.Helper()
	db = setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	crewID = seedCrewRow(t, db, "crew-ts-"+t.Name(), wsID, "Crew", "crew-ts")
	missionID = seedMissionRow(t, db, "mis-ts", wsID, crewID, "M")
	h = NewMissionHandler(db, nil, nil, newTestLogger())
	return h, db, userID, wsID, crewID, missionID
}

// covTSReq builds a state-transition request with path values + workspace user.
func covTSReq(t *testing.T, method, userID, wsID, crewID, missionID, role string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/missions", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("missionId", missionID)
	req = withWorkspaceUser(req, userID, wsID, role)
	return req, httptest.NewRecorder()
}

// --- Restart -----------------------------------------------------------------

func TestCovTSRestart_Forbidden(t *testing.T) {
	h, _, userID, wsID, crewID, missionID := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "VIEWER")
	h.Restart(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSRestart_NotFound(t *testing.T) {
	h, _, userID, wsID, crewID, _ := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, "does-not-exist", "OWNER")
	h.Restart(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSRestart_InvalidState(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	// PLANNING is not a terminal state → CAS claims 0 rows → 409.
	covTSSetMissionStatus(t, db, missionID, "PLANNING")
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Restart(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSRestart_HappyPath(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "rt-done", missionID, "Done", "COMPLETED", 0, "[]")
	covTSInsertTask(t, db, "rt-fail", missionID, "Failed", "FAILED", 1, "[]")
	covTSInsertTask(t, db, "rt-dep", missionID, "Dep", "BLOCKED", 2, `["rt-done"]`)

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Restart(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if got := covTSMissionStatus(t, db, missionID); got != "PLANNING" {
		t.Fatalf("mission status = %s, want PLANNING", got)
	}
	if got := covTSTaskStatus(t, db, "rt-done"); got != "COMPLETED" {
		t.Fatalf("completed task changed to %s", got)
	}
	if got := covTSTaskStatus(t, db, "rt-fail"); got != "PENDING" {
		t.Fatalf("failed task = %s, want PENDING", got)
	}
	// rt-dep depends only on rt-done which is COMPLETED → unblocked to PENDING.
	if got := covTSTaskStatus(t, db, "rt-dep"); got != "PENDING" {
		t.Fatalf("dep task = %s, want PENDING (deps complete)", got)
	}
}

// --- Resume ------------------------------------------------------------------

func TestCovTSResume_Forbidden(t *testing.T) {
	h, _, userID, wsID, crewID, missionID := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "VIEWER")
	h.Resume(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSResume_NotFound(t *testing.T) {
	h, _, userID, wsID, crewID, _ := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, "does-not-exist", "OWNER")
	h.Resume(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSResume_WrongState(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	// Only FAILED missions can be resumed; PLANNING → 409.
	covTSSetMissionStatus(t, db, missionID, "PLANNING")
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSResume_NoFailedTasks(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	// Mission is FAILED but no task is FAILED/AWAITING_APPROVAL → 400.
	covTSInsertTask(t, db, "rsnf-1", missionID, "T1", "COMPLETED", 0, "[]")

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	// On the no-tasks-to-reset path the deferred rollback restores FAILED.
	if got := covTSMissionStatus(t, db, missionID); got != "FAILED" {
		t.Fatalf("mission status = %s, want FAILED (rolled back)", got)
	}
}

func TestCovTSResume_HappyPath(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSSetMissionStatus(t, db, missionID, "FAILED")
	covTSInsertTask(t, db, "rs-done", missionID, "Done", "COMPLETED", 0, "[]")
	covTSInsertTask(t, db, "rs-fail", missionID, "Failed", "FAILED", 1, "[]")
	// Downstream dependent of the failed task must cascade-reset.
	covTSInsertTask(t, db, "rs-child", missionID, "Child", "BLOCKED", 2, `["rs-fail"]`)

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Resume(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	// missionEngine is nil → transitions to IN_PROGRESS without engine start.
	if got := covTSMissionStatus(t, db, missionID); got != "IN_PROGRESS" {
		t.Fatalf("mission status = %s, want IN_PROGRESS", got)
	}
	if got := covTSTaskStatus(t, db, "rs-done"); got != "COMPLETED" {
		t.Fatalf("completed task changed to %s", got)
	}
	// rs-fail has no deps → PENDING.
	if got := covTSTaskStatus(t, db, "rs-fail"); got != "PENDING" {
		t.Fatalf("failed task = %s, want PENDING", got)
	}
	// rs-child depends on rs-fail (now RESET) → BLOCKED.
	if got := covTSTaskStatus(t, db, "rs-child"); got != "BLOCKED" {
		t.Fatalf("child task = %s, want BLOCKED", got)
	}
}

// --- Clone -------------------------------------------------------------------

func TestCovTSClone_Forbidden(t *testing.T) {
	h, _, userID, wsID, crewID, missionID := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "VIEWER")
	h.Clone(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSClone_NotFound(t *testing.T) {
	h, _, userID, wsID, crewID, _ := covTSFixture(t)
	req, rr := covTSReq(t, "POST", userID, wsID, crewID, "does-not-exist", "OWNER")
	h.Clone(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTSClone_HappyPath(t *testing.T) {
	h, db, userID, wsID, crewID, missionID := covTSFixture(t)
	covTSInsertTask(t, db, "cl-a", missionID, "A", "PENDING", 0, "[]")
	covTSInsertTask(t, db, "cl-b", missionID, "B", "BLOCKED", 1, `["cl-a"]`)

	req, rr := covTSReq(t, "POST", userID, wsID, crewID, missionID, "OWNER")
	h.Clone(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body=%s", rr.Code, rr.Body.String())
	}

	// A new mission (PLANNING) plus a "(copy)" title should now exist.
	var newID, newTitle, newStatus string
	if err := db.QueryRow(
		`SELECT id, title, status FROM missions WHERE id != ? AND crew_id = ?`,
		missionID, crewID).Scan(&newID, &newTitle, &newStatus); err != nil {
		t.Fatalf("read cloned mission: %v", err)
	}
	if newStatus != "PLANNING" {
		t.Fatalf("clone status = %s, want PLANNING", newStatus)
	}
	if newTitle != "M (copy)" {
		t.Fatalf("clone title = %q, want \"M (copy)\"", newTitle)
	}

	// Two tasks copied with remapped deps: the dependent task stays BLOCKED,
	// the root task PENDING. New task IDs differ from originals.
	var nTasks, nBlocked, nPending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, newID).Scan(&nTasks); err != nil {
		t.Fatalf("count clone tasks: %v", err)
	}
	if nTasks != 2 {
		t.Fatalf("clone task count = %d, want 2", nTasks)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`, newID).Scan(&nBlocked)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ? AND status = 'PENDING'`, newID).Scan(&nPending)
	if nBlocked != 1 || nPending != 1 {
		t.Fatalf("clone task statuses blocked=%d pending=%d, want 1/1", nBlocked, nPending)
	}
	// Cloned deps must point at the new task IDs, not the originals.
	var leakedOldDep int
	_ = db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ? AND depends_on LIKE '%cl-a%'`, newID).Scan(&leakedOldDep)
	if leakedOldDep != 0 {
		t.Fatalf("clone leaked original task id in depends_on")
	}

	// A synthetic MISSION chat should have been created for the clone.
	var nChat int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chats WHERE id = ? AND mode = 'MISSION'`, newID).Scan(&nChat); err != nil {
		t.Fatalf("count clone chat: %v", err)
	}
	if nChat != 1 {
		t.Fatalf("clone chat count = %d, want 1", nChat)
	}
}
