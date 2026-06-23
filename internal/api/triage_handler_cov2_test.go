package api

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// triage_handler_cov2_test.go picks up the DB-error and null/set update
// arms that triage_handler_cov_test.go skipped. Reuses covTriHandler /
// covTriSeedRule / covTriSeedIssueTitle. Prefix covTri2.

func covTri2Patch(t *testing.T, h *TriageHandler, wsID, ruleID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/"+ruleID, bytes.NewBufferString(body))
	req.SetPathValue("ruleId", ruleID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.UpdateRule(rr, req)
	return rr
}

func covTri2Seed(t *testing.T, db *sql.DB, wsID string) (crewID, agentID, projectID string) {
	t.Helper()
	crewID = "covtri2-crew"
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'T', 'covtri2-c')`, crewID, wsID)
	agentID = seedAgentRow(t, db, "covtri2-ag", wsID, crewID, "T", "covtri2-a", "AGENT")
	projectID = "covtri2-proj"
	execOrFatal(t, db, `INSERT INTO projects (id, workspace_id, name, slug, created_at, updated_at) VALUES (?, ?, 'P', 'covtri2-p', datetime('now'), datetime('now'))`, projectID, wsID)
	return crewID, agentID, projectID
}

func TestCovTri2_UpdateRule_SetAndNullArms(t *testing.T) {
	h, db, _, wsID := covTriHandler(t)
	crewID, agentID, projectID := covTri2Seed(t, db, wsID)
	ruleID := covTriSeedRule(t, db, wsID, "r1", "bug", "contains", true, 1)

	// Set all three ids to concrete values.
	rr := covTri2Patch(t, h, wsID, ruleID,
		`{"crew_id":"`+crewID+`","assignee_id":"`+agentID+`","project_id":"`+projectID+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("set arm: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var crew, assignee, project sql.NullString
	if err := db.QueryRow(`SELECT crew_id, assignee_id, project_id FROM triage_rules WHERE id = ?`, ruleID).Scan(&crew, &assignee, &project); err != nil {
		t.Fatalf("read rule: %v", err)
	}
	if crew.String != crewID || assignee.String != agentID || project.String != projectID {
		t.Errorf("rule = %v/%v/%v", crew, assignee, project)
	}

	// Clear them with explicit "".
	rr = covTri2Patch(t, h, wsID, ruleID, `{"crew_id":"","assignee_id":"","project_id":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("null arm: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if err := db.QueryRow(`SELECT crew_id, assignee_id, project_id FROM triage_rules WHERE id = ?`, ruleID).Scan(&crew, &assignee, &project); err != nil {
		t.Fatalf("read rule: %v", err)
	}
	if crew.Valid || assignee.Valid || project.Valid {
		t.Errorf("ids not cleared: %v/%v/%v", crew, assignee, project)
	}
}

func TestCovTri2_UpdateRule_ExecDBError(t *testing.T) {
	h, db, _, wsID := covTriHandler(t)
	ruleID := covTriSeedRule(t, db, wsID, "r2", "bug", "contains", true, 1)
	execOrFatal(t, db, `CREATE TRIGGER covtri2_fail_upd BEFORE UPDATE ON triage_rules BEGIN SELECT RAISE(ABORT, 'covtri2 boom'); END`)
	rr := covTri2Patch(t, h, wsID, ruleID, `{"name":"renamed"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovTri2_Process_LoadIssuesDBError(t *testing.T) {
	h, db, _, wsID := covTriHandler(t)
	covTriSeedRule(t, db, wsID, "r3", "bug", "contains", true, 1)
	execOrFatal(t, db, `ALTER TABLE missions RENAME TO covtri2_missions_bak`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Process(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// covTri2SeedFullRule inserts an enabled rule with priority + project so
// Process has concrete columns to SET on a matched issue.
func covTri2SeedFullRule(t *testing.T, db *sql.DB, wsID, id, pattern, projectID string) {
	t.Helper()
	execOrFatal(t, db, `
		INSERT INTO triage_rules (id, workspace_id, name, pattern, match_type,
		    priority, project_id, position, enabled, match_count, created_at)
		VALUES (?, ?, ?, ?, 'contains', 'high', ?, 1, 1, 0, datetime('now'))`,
		id, wsID, id, pattern, projectID)
}

func TestCovTri2_Process_IssueUpdateFailureSkips(t *testing.T) {
	h, db, _, wsID := covTriHandler(t)
	crewID, _, projectID := covTri2Seed(t, db, wsID)
	covTri2SeedFullRule(t, db, wsID, "covtri2-r4", "bug", projectID)
	covTriSeedIssueTitle(t, db, wsID, crewID, "covtri2-ag", "A bug to be triaged")

	// Updating the matched issue fails -> the per-rule continue arm.
	execOrFatal(t, db, `CREATE TRIGGER covtri2_fail_missions BEFORE UPDATE ON missions BEGIN SELECT RAISE(ABORT, 'covtri2 boom'); END`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Process(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (per-issue failures are skipped); body=%s", rr.Code, rr.Body.String())
	}
	var issueProject sql.NullString
	if err := db.QueryRow(`SELECT project_id FROM missions WHERE workspace_id = ? AND mission_type = 'issue'`, wsID).Scan(&issueProject); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if issueProject.Valid {
		t.Errorf("issue project = %v, want unset (update failed)", issueProject)
	}
}

func TestCovTri2_Process_MatchCountFailureNonFatal(t *testing.T) {
	h, db, _, wsID := covTriHandler(t)
	crewID, _, projectID := covTri2Seed(t, db, wsID)
	covTri2SeedFullRule(t, db, wsID, "covtri2-r5", "bug", projectID)
	covTriSeedIssueTitle(t, db, wsID, crewID, "covtri2-ag", "Another bug to triage")

	// The issue update succeeds; only the match_count bump fails.
	execOrFatal(t, db, `CREATE TRIGGER covtri2_fail_rules BEFORE UPDATE ON triage_rules BEGIN SELECT RAISE(ABORT, 'covtri2 boom'); END`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Process(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (match_count failure non-fatal); body=%s", rr.Code, rr.Body.String())
	}
	var issueProject sql.NullString
	if err := db.QueryRow(`SELECT project_id FROM missions WHERE workspace_id = ? AND mission_type = 'issue'`, wsID).Scan(&issueProject); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if issueProject.String != projectID {
		t.Errorf("issue project = %v, want %s (rule applied)", issueProject, projectID)
	}
}
