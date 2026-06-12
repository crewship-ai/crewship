package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recurring_issue_handler_cov_test.go — remaining branches: the
// empty-list normalization, the INSERT failure, the UPDATE failure,
// the read-back race, and the DELETE failure. Helpers prefixed covRI.

func covRIFixture(t *testing.T) (*RecurringIssueHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "covri-crew", wsID, "Crew", "covri-crew")
	return NewRecurringIssueHandler(db, nil, newTestLogger()), userID, wsID, crewID
}

func covRISeed(t *testing.T, h *RecurringIssueHandler, id, wsID, crewID string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO recurring_issues
		(id, workspace_id, crew_id, title, cron_expression, enabled, run_count, created_at)
		VALUES (?, ?, ?, 'Weekly chores', '0 9 * * 1', 1, 0, datetime('now'))`,
		id, wsID, crewID)
}

func TestCovRI_List_Empty_ReturnsEmptyArray(t *testing.T) {
	h, userID, wsID, _ := covRIFixture(t)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/recurring-issues", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rr.Body.String())
	}
}

func TestCovRI_Create_InsertFailure_500(t *testing.T) {
	h, userID, wsID, crewID := covRIFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covri_block_ins BEFORE INSERT ON recurring_issues
		BEGIN SELECT RAISE(ABORT, 'covri forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/recurring-issues", jsonBody(map[string]any{
			"crew_id": crewID, "title": "T", "cron_expression": "0 9 * * 1",
		})),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRI_Update_ExecError_500(t *testing.T) {
	h, userID, wsID, crewID := covRIFixture(t)
	covRISeed(t, h, "covri-r1", wsID, crewID)
	execOrFatal(t, h.db, `CREATE TRIGGER covri_block_upd BEFORE UPDATE ON recurring_issues
		BEGIN SELECT RAISE(ABORT, 'covri forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/recurring-issues/covri-r1",
			jsonBody(map[string]any{"title": "Renamed", "project_id": ""})),
		userID, wsID, "OWNER")
	req.SetPathValue("recurringId", "covri-r1")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRI_Update_ReadBackRaces_500(t *testing.T) {
	h, userID, wsID, crewID := covRIFixture(t)
	covRISeed(t, h, "covri-r2", wsID, crewID)
	execOrFatal(t, h.db, `CREATE TRIGGER covri_vanish AFTER UPDATE ON recurring_issues
		BEGIN DELETE FROM recurring_issues WHERE id = NEW.id; END`)
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/recurring-issues/covri-r2",
			jsonBody(map[string]any{"title": "Renamed"})),
		userID, wsID, "OWNER")
	req.SetPathValue("recurringId", "covri-r2")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovRI_Delete_ExecError_500(t *testing.T) {
	h, userID, wsID, crewID := covRIFixture(t)
	covRISeed(t, h, "covri-r3", wsID, crewID)
	execOrFatal(t, h.db, `CREATE TRIGGER covri_block_del BEFORE DELETE ON recurring_issues
		BEGIN SELECT RAISE(ABORT, 'covri forced'); END`)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/recurring-issues/covri-r3", nil),
		userID, wsID, "OWNER")
	req.SetPathValue("recurringId", "covri-r3")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
