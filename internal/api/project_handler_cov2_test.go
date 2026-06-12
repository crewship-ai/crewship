package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// project_handler_cov2_test.go — second pass: the Update write/read
// failures, the read-back progress computation, the Delete cascade
// failure arms, and the lost-delete 404. Helpers prefixed covPH2.

func covPH2Fixture(t *testing.T) (*ProjectHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	_ = crewID
	_ = leadID
	return NewProjectHandler(db, nil, newTestLogger()), userID, wsID, crewID
}

func covPH2Patch(h *ProjectHandler, userID, wsID, projectID string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/projects/"+projectID, jsonBody(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("projectId", projectID)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

func TestCovPH2_Update_ExecError_500(t *testing.T) {
	h, userID, wsID, _ := covPH2Fixture(t)
	projectID := seedProject(t, h.db, wsID, "covph2-p1")
	execOrFatal(t, h.db, `CREATE TRIGGER covph2_block_upd BEFORE UPDATE ON projects
		BEGIN SELECT RAISE(ABORT, 'covph2 forced'); END`)
	rr := covPH2Patch(h, userID, wsID, projectID, map[string]any{"name": "Renamed"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPH2_Update_ReadBackRaces_500(t *testing.T) {
	h, userID, wsID, _ := covPH2Fixture(t)
	projectID := seedProject(t, h.db, wsID, "covph2-p2")
	execOrFatal(t, h.db, `CREATE TRIGGER covph2_vanish AFTER UPDATE ON projects
		BEGIN DELETE FROM projects WHERE id = NEW.id; END`)
	rr := covPH2Patch(h, userID, wsID, projectID, map[string]any{"name": "Renamed"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovPH2_Update_ProgressComputedFromIssues — a project with one
// DONE issue out of two reports progress 50 on the updated payload.
func TestCovPH2_Update_ProgressComputedFromIssues(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewProjectHandler(db, nil, newTestLogger())
	projectID := seedProject(t, db, wsID, "covph2-p3")
	i1 := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "DONE")
	i2 := seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET project_id = ? WHERE id IN (?, ?)`, projectID, i1, i2)

	rr := covPH2Patch(h, userID, wsID, projectID, map[string]any{"name": "Renamed"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Progress   int `json:"progress"`
		IssueCount int `json:"issue_count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IssueCount != 2 || resp.Progress != 50 {
		t.Errorf("issue_count=%d progress=%d, want 2/50", resp.IssueCount, resp.Progress)
	}
}

func covPH2Delete(h *ProjectHandler, userID, wsID, projectID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/projects/"+projectID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("projectId", projectID)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestCovPH2_Delete_UnlinkMissionsFailure_500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewProjectHandler(db, nil, newTestLogger())
	projectID := seedProject(t, db, wsID, "covph2-p4")
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `UPDATE missions SET project_id = ? WHERE id = ?`, projectID, issueID)
	execOrFatal(t, db, `CREATE TRIGGER covph2_block_unlink BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT, 'covph2 forced'); END`)

	rr := covPH2Delete(h, userID, wsID, projectID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	// Tx rollback: project still exists.
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&n); err != nil || n != 1 {
		t.Errorf("projects = %d err=%v, want survivor after rollback", n, err)
	}
}

func TestCovPH2_Delete_ProjectDeleteFailure_500(t *testing.T) {
	h, userID, wsID, _ := covPH2Fixture(t)
	projectID := seedProject(t, h.db, wsID, "covph2-p5")
	execOrFatal(t, h.db, `CREATE TRIGGER covph2_block_del BEFORE DELETE ON projects
		BEGIN SELECT RAISE(ABORT, 'covph2 forced'); END`)
	rr := covPH2Delete(h, userID, wsID, projectID)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPH2_Delete_ZeroRows_404(t *testing.T) {
	h, userID, wsID, _ := covPH2Fixture(t)
	projectID := seedProject(t, h.db, wsID, "covph2-p6")
	execOrFatal(t, h.db, `CREATE TRIGGER covph2_ignore_del BEFORE DELETE ON projects
		BEGIN SELECT RAISE(IGNORE); END`)
	rr := covPH2Delete(h, userID, wsID, projectID)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}
