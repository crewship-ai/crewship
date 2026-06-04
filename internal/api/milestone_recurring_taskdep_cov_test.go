package api

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covMRTMilestoneHandler builds a MilestoneHandler with a seeded project.
// Returns handler, userID, wsID, projectID.
func covMRTMilestoneHandler(t *testing.T) (*MilestoneHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	pid := seedProject(t, db, wsID, "cov-mrt-ms")
	return NewMilestoneHandler(db, nil, newTestLogger()), userID, wsID, pid
}

// covMRTRecurringHandler builds a RecurringIssueHandler with a seeded crew.
// Returns handler, userID, wsID, crewID.
func covMRTRecurringHandler(t *testing.T) (*RecurringIssueHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, _, _ := seedIssueFixtures(t, db)
	return NewRecurringIssueHandler(db, nil, newTestLogger()), userID, wsID, crewID
}

// covMRTInsertTask inserts a mission_task row directly.
func covMRTInsertTask(t *testing.T, db *sql.DB, id, missionID, status, dependsOn string) {
	t.Helper()
	if dependsOn == "" {
		dependsOn = "[]"
	}
	_, err := db.Exec(`INSERT INTO mission_tasks(id,mission_id,title,status,task_order,depends_on,created_at,updated_at)
		VALUES (?,?,?,?,?,?,datetime('now'),datetime('now'))`,
		id, missionID, "task-"+id, status, 1, dependsOn)
	if err != nil {
		t.Fatalf("insert mission_task %s: %v", id, err)
	}
}

// covMRTTaskStatus reads a mission_task's current status.
func covMRTTaskStatus(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var st string
	if err := db.QueryRow(`SELECT status FROM mission_tasks WHERE id = ?`, id).Scan(&st); err != nil {
		t.Fatalf("read status %s: %v", id, err)
	}
	return st
}

// ── milestone_handler.go ───────────────────────────────────────────────────

// TestCovMRTMilestoneUpdateEachField exercises every optional setter branch in
// Update (name/description/target_date/status/position) and asserts the row was
// persisted with the new values via the returned response.
func TestCovMRTMilestoneUpdateEachField(t *testing.T) {
	h, userID, wsID, pid := covMRTMilestoneHandler(t)

	// Create a milestone to update.
	body := bytes.NewBufferString(`{"name":"orig"}`)
	cReq := httptest.NewRequest("POST", "/", body)
	cReq.SetPathValue("projectId", pid)
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	if cRR.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", cRR.Code, cRR.Body.String())
	}
	var created milestoneResponse
	mustUnmarshal(t, cRR, &created)

	upd := bytes.NewBufferString(`{"name":"renamed","description":"desc","target_date":"2031-05-05","status":"completed","position":7}`)
	uReq := httptest.NewRequest("PATCH", "/", upd)
	uReq.SetPathValue("milestoneId", created.ID)
	uReq = withWorkspaceUser(uReq, userID, wsID, "OWNER")
	uRR := httptest.NewRecorder()
	h.Update(uRR, uReq)
	if uRR.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", uRR.Code, uRR.Body.String())
	}
	var got milestoneResponse
	mustUnmarshal(t, uRR, &got)
	if got.Name != "renamed" || got.Status != "completed" || got.Position != 7 {
		t.Errorf("unexpected updated milestone: %+v", got)
	}
	if got.Description == nil || *got.Description != "desc" {
		t.Errorf("description not persisted: %+v", got.Description)
	}
	if got.TargetDate == nil || *got.TargetDate != "2031-05-05" {
		t.Errorf("target_date not persisted: %+v", got.TargetDate)
	}
}

// TestCovMRTMilestoneUpdateNoFields covers the empty-update 400 branch.
func TestCovMRTMilestoneUpdateNoFields(t *testing.T) {
	h, userID, wsID, pid := covMRTMilestoneHandler(t)

	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x"}`))
	cReq.SetPathValue("projectId", pid)
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	var created milestoneResponse
	mustUnmarshal(t, cRR, &created)

	uReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	uReq.SetPathValue("milestoneId", created.ID)
	uReq = withWorkspaceUser(uReq, userID, wsID, "OWNER")
	uRR := httptest.NewRecorder()
	h.Update(uRR, uReq)
	if uRR.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", uRR.Code)
	}
}

// TestCovMRTMilestoneUpdateBadJSON covers the invalid-JSON 400 branch on Update.
func TestCovMRTMilestoneUpdateBadJSON(t *testing.T) {
	h, userID, wsID, pid := covMRTMilestoneHandler(t)

	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"x"}`))
	cReq.SetPathValue("projectId", pid)
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	var created milestoneResponse
	mustUnmarshal(t, cRR, &created)

	uReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`not json`))
	uReq.SetPathValue("milestoneId", created.ID)
	uReq = withWorkspaceUser(uReq, userID, wsID, "OWNER")
	uRR := httptest.NewRecorder()
	h.Update(uRR, uReq)
	if uRR.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", uRR.Code)
	}
}

// TestCovMRTMilestoneUpdateForbidden covers the requireRole("create") guard.
func TestCovMRTMilestoneUpdateForbidden(t *testing.T) {
	h, userID, wsID, _ := covMRTMilestoneHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("milestoneId", "anything")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestCovMRTMilestoneDeleteForbidden covers the requireRole("manage") guard.
func TestCovMRTMilestoneDeleteForbidden(t *testing.T) {
	h, userID, wsID, _ := covMRTMilestoneHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("milestoneId", "anything")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestCovMRTMilestoneDeleteUnlinksMissions covers the happy delete path with a
// linked issue: the milestone is removed and the mission's milestone_id is set
// to NULL.
func TestCovMRTMilestoneDeleteUnlinksMissions(t *testing.T) {
	h, userID, wsID, pid := covMRTMilestoneHandler(t)
	db := h.db

	// Create a milestone.
	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"to-delete"}`))
	cReq.SetPathValue("projectId", pid)
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	var created milestoneResponse
	mustUnmarshal(t, cRR, &created)

	// Seed an issue and link it to the milestone.
	crewID := seedCrewRow(t, db, "cov-mrt-crew", wsID, "Crew", "crew")
	leadID := seedAgentRow(t, db, "cov-mrt-lead", wsID, crewID, "Lead", "lead", "LEAD")
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := db.Exec(`UPDATE missions SET milestone_id = ? WHERE id = ?`, created.ID, issueID); err != nil {
		t.Fatalf("link issue to milestone: %v", err)
	}

	dReq := httptest.NewRequest("DELETE", "/", nil)
	dReq.SetPathValue("milestoneId", created.ID)
	dReq = withWorkspaceUser(dReq, userID, wsID, "OWNER")
	dRR := httptest.NewRecorder()
	h.Delete(dRR, dReq)
	if dRR.Code != http.StatusNoContent {
		t.Fatalf("delete: %d body=%s", dRR.Code, dRR.Body.String())
	}

	// Milestone gone.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM milestones WHERE id = ?`, created.ID).Scan(&n); err != nil {
		t.Fatalf("count milestones: %v", err)
	}
	if n != 0 {
		t.Errorf("milestone still present, count=%d", n)
	}
	// Mission unlinked.
	var ms sql.NullString
	if err := db.QueryRow(`SELECT milestone_id FROM missions WHERE id = ?`, issueID).Scan(&ms); err != nil {
		t.Fatalf("read mission milestone_id: %v", err)
	}
	if ms.Valid {
		t.Errorf("mission milestone_id not nulled: %q", ms.String)
	}
}

// TestCovMRTMilestoneListWithCounts covers the List happy path including the
// issue_count / done_count aggregation join.
func TestCovMRTMilestoneListWithCounts(t *testing.T) {
	h, userID, wsID, pid := covMRTMilestoneHandler(t)
	db := h.db

	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"name":"counted"}`))
	cReq.SetPathValue("projectId", pid)
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	var created milestoneResponse
	mustUnmarshal(t, cRR, &created)

	crewID := seedCrewRow(t, db, "cov-mrt-crew2", wsID, "Crew2", "crew2")
	leadID := seedAgentRow(t, db, "cov-mrt-lead2", wsID, crewID, "Lead2", "lead2", "LEAD")
	open := seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	done := seedIssue(t, db, wsID, crewID, leadID, "ENG-3", "DONE")
	for _, id := range []string{open, done} {
		if _, err := db.Exec(`UPDATE missions SET milestone_id = ? WHERE id = ?`, created.ID, id); err != nil {
			t.Fatalf("link issue: %v", err)
		}
	}

	lReq := httptest.NewRequest("GET", "/", nil)
	lReq.SetPathValue("projectId", pid)
	lReq = withWorkspaceUser(lReq, userID, wsID, "OWNER")
	lRR := httptest.NewRecorder()
	h.List(lRR, lReq)
	if lRR.Code != http.StatusOK {
		t.Fatalf("list: %d body=%s", lRR.Code, lRR.Body.String())
	}
	var list []milestoneResponse
	mustUnmarshal(t, lRR, &list)
	if len(list) != 1 {
		t.Fatalf("list len = %d want 1", len(list))
	}
	if list[0].IssueCount != 2 || list[0].DoneCount != 1 {
		t.Errorf("counts = issue:%d done:%d, want 2/1", list[0].IssueCount, list[0].DoneCount)
	}
}

// ── recurring_issue_handler.go ──────────────────────────────────────────────

// TestCovMRTRecurringDeleteForbidden covers the requireRole("manage") guard on Delete.
func TestCovMRTRecurringDeleteForbidden(t *testing.T) {
	h, userID, wsID, _ := covMRTRecurringHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", "anything")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// TestCovMRTRecurringUpdateBadJSON covers the invalid-JSON 400 branch on Update
// (the existing record must be found first).
func TestCovMRTRecurringUpdateBadJSON(t *testing.T) {
	h, userID, wsID, crewID := covMRTRecurringHandler(t)

	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"crew_id":"`+crewID+`","title":"x","cron_expression":"0 9 * * *"}`))
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	var resp recurringIssueResponse
	mustUnmarshal(t, cRR, &resp)

	uReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`not json`))
	uReq.SetPathValue("id", resp.ID)
	uReq = withWorkspaceUser(uReq, userID, wsID, "OWNER")
	uRR := httptest.NewRecorder()
	h.Update(uRR, uReq)
	if uRR.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", uRR.Code)
	}
}

// TestCovMRTRecurringUpdateNullableFields exercises the project_id / milestone_id
// SetNull-vs-Set branches plus the cron recompute path, asserting persisted state.
func TestCovMRTRecurringUpdateNullableFields(t *testing.T) {
	h, userID, wsID, crewID := covMRTRecurringHandler(t)
	db := h.db
	pid := seedProject(t, db, wsID, "cov-mrt-ri-proj")

	// Seed a real milestone so the milestone_id FK is satisfiable.
	msID := generateCUID()
	if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name, status, position, created_at, updated_at)
		VALUES (?, ?, 'cov-ms', 'active', 1, datetime('now'), datetime('now'))`, msID, pid); err != nil {
		t.Fatalf("seed milestone: %v", err)
	}

	// Create with project_id set so the SetNull branch has something to clear.
	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"crew_id":"`+crewID+`","title":"x","cron_expression":"0 9 * * *","project_id":"`+pid+`"}`))
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	if cRR.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", cRR.Code, cRR.Body.String())
	}
	var resp recurringIssueResponse
	mustUnmarshal(t, cRR, &resp)

	// Set milestone_id to a non-empty value (Set branch) and clear project_id
	// (SetNull branch), and change cron (recompute next_run).
	upd := `{"project_id":"","milestone_id":"` + msID + `","cron_expression":"15 10 * * *","crew_id":"` + crewID + `"}`
	uReq := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(upd))
	uReq.SetPathValue("id", resp.ID)
	uReq = withWorkspaceUser(uReq, userID, wsID, "OWNER")
	uRR := httptest.NewRecorder()
	h.Update(uRR, uReq)
	if uRR.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", uRR.Code, uRR.Body.String())
	}
	var got recurringIssueResponse
	mustUnmarshal(t, uRR, &got)
	if got.ProjectID != nil {
		t.Errorf("project_id should be null, got %v", *got.ProjectID)
	}
	if got.MilestoneID == nil || *got.MilestoneID != msID {
		t.Errorf("milestone_id not set: %+v", got.MilestoneID)
	}
	if got.CronExpression != "15 10 * * *" {
		t.Errorf("cron_expression = %q, want updated", got.CronExpression)
	}
}

// TestCovMRTRecurringListNoFilter covers the List path without a crew_id query
// filter (the un-filtered branch) and asserts results are returned.
func TestCovMRTRecurringListNoFilter(t *testing.T) {
	h, userID, wsID, crewID := covMRTRecurringHandler(t)

	cReq := httptest.NewRequest("POST", "/", bytes.NewBufferString(
		`{"crew_id":"`+crewID+`","title":"listed","cron_expression":"0 9 * * *"}`))
	cReq = withWorkspaceUser(cReq, userID, wsID, "OWNER")
	cRR := httptest.NewRecorder()
	h.Create(cRR, cReq)
	if cRR.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", cRR.Code, cRR.Body.String())
	}

	lReq := httptest.NewRequest("GET", "/", nil)
	lReq = withWorkspaceUser(lReq, userID, wsID, "OWNER")
	lRR := httptest.NewRecorder()
	h.List(lRR, lReq)
	if lRR.Code != http.StatusOK {
		t.Fatalf("list: %d", lRR.Code)
	}
	var list []recurringIssueResponse
	mustUnmarshal(t, lRR, &list)
	if len(list) != 1 || list[0].CrewName == "" {
		t.Errorf("list = %+v, want one with crew name joined", list)
	}
}

// ── task_dependency.go ──────────────────────────────────────────────────────

// TestCovMRTUnblockDependentTasks covers unblockDependentTasks: a BLOCKED task
// whose sole dependency is COMPLETED transitions to PENDING and broadcasts.
func TestCovMRTUnblockDependentTasks(t *testing.T) {
	h, _, wsID, _, _, missionID := newMissionHandlerForTasks(t)
	db := h.db

	done := generateCUID()
	blocked := generateCUID()
	other := generateCUID()
	covMRTInsertTask(t, db, done, missionID, "COMPLETED", "[]")
	covMRTInsertTask(t, db, blocked, missionID, "BLOCKED", `["`+done+`"]`)
	// `other` depends on a still-incomplete task → must stay BLOCKED.
	covMRTInsertTask(t, db, other, missionID, "BLOCKED", `["`+blocked+`"]`)

	req := httptest.NewRequest("POST", "/", nil)
	req = withWorkspaceUser(req, "u", wsID, "OWNER")
	h.unblockDependentTasks(req, missionID, done)

	if got := covMRTTaskStatus(t, db, blocked); got != "PENDING" {
		t.Errorf("blocked task status = %q, want PENDING", got)
	}
	if got := covMRTTaskStatus(t, db, other); got != "BLOCKED" {
		t.Errorf("other task status = %q, want still BLOCKED", got)
	}
}

// TestCovMRTUnblockCompletedDeps covers unblockCompletedDeps: the no-filter
// sweep that flips all satisfiable BLOCKED tasks to PENDING.
func TestCovMRTUnblockCompletedDeps(t *testing.T) {
	h, _, wsID, _, _, missionID := newMissionHandlerForTasks(t)
	db := h.db

	a := generateCUID()
	b := generateCUID()
	covMRTInsertTask(t, db, a, missionID, "COMPLETED", "[]")
	covMRTInsertTask(t, db, b, missionID, "BLOCKED", `["`+a+`"]`)

	req := httptest.NewRequest("POST", "/", nil)
	req = withWorkspaceUser(req, "u", wsID, "OWNER")
	h.unblockCompletedDeps(req, missionID)

	if got := covMRTTaskStatus(t, db, b); got != "PENDING" {
		t.Errorf("task b status = %q, want PENDING", got)
	}
}

// TestCovMRTUnblockNoCandidates covers the early-return path of the unblock
// helpers when there are no BLOCKED tasks (findUnblockableTasks returns nil).
func TestCovMRTUnblockNoCandidates(t *testing.T) {
	h, _, wsID, _, _, missionID := newMissionHandlerForTasks(t)
	db := h.db

	only := generateCUID()
	covMRTInsertTask(t, db, only, missionID, "COMPLETED", "[]")

	req := httptest.NewRequest("POST", "/", nil)
	req = withWorkspaceUser(req, "u", wsID, "OWNER")
	// Neither call should change anything or panic.
	h.unblockDependentTasks(req, missionID, only)
	h.unblockCompletedDeps(req, missionID)

	if got := covMRTTaskStatus(t, db, only); got != "COMPLETED" {
		t.Errorf("status = %q, want COMPLETED unchanged", got)
	}
}
