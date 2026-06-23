package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issue_handler_update_cov_test.go — remaining Update/Delete branches:
// the clear-vs-set arms for project/milestone/parent, the routine
// validation DB error, completed_at on DONE, write failures via
// triggers, label replacement warn paths, and the Delete guards.
// Helpers prefixed covIHU.

func covIHUPatch(h *IssueHandler, userID, wsID, crewID, ident string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/issues/"+ident, jsonBody(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	return rr
}

func TestCovIHU_Update_ClearAndSetRelations(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	parentID := seedIssue(t, db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{
		"project_id":      "",
		"milestone_id":    "",
		"parent_issue_id": parentID,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var gotParent string
	if err := db.QueryRow(`SELECT parent_issue_id FROM missions WHERE identifier = 'ENG-1'`).Scan(&gotParent); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if gotParent != parentID {
		t.Errorf("parent_issue_id = %q, want %q", gotParent, parentID)
	}
}

func TestCovIHU_Update_RoutineValidateDBError_500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `ALTER TABLE pipelines RENAME TO pipelines_broken`)

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"routine_id": "rt-1"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIHU_Update_StatusDone_SetsCompletedAt(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"status": "DONE"})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var completedAt *string
	if err := db.QueryRow(`SELECT completed_at FROM missions WHERE identifier = 'ENG-1'`).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completedAt == nil || *completedAt == "" {
		t.Errorf("completed_at empty after DONE transition")
	}
}

func TestCovIHU_Update_ExecError_500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `CREATE TRIGGER covihu_block_upd BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT, 'covihu forced'); END`)

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"title": "New title"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovIHU_Update_LabelWriteFailures_NonFatal — label replacement is
// best-effort: blocked DELETE/INSERT on mission_labels only log, the
// PATCH still answers 200 with the issue body.
func TestCovIHU_Update_LabelWriteFailures_NonFatal(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `INSERT INTO labels (id, workspace_id, name, color, created_at)
		VALUES ('covihu-l1', ?, 'bug', '#f00', datetime('now'))`, wsID)
	execOrFatal(t, db, `INSERT INTO labels (id, workspace_id, name, color, created_at)
		VALUES ('covihu-l2', ?, 'infra', '#0f0', datetime('now'))`, wsID)
	execOrFatal(t, db, `INSERT INTO mission_labels (mission_id, label_id) VALUES (?, 'covihu-l1')`, missionID)
	execOrFatal(t, db, `CREATE TRIGGER covihu_block_ml_del BEFORE DELETE ON mission_labels
		BEGIN SELECT RAISE(ABORT, 'covihu forced'); END`)
	execOrFatal(t, db, `CREATE TRIGGER covihu_block_ml_ins BEFORE INSERT ON mission_labels
		BEGIN SELECT RAISE(ABORT, 'covihu forced'); END`)

	rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"labels": []string{"covihu-l2"}})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (label writes are best-effort); body=%s",
			rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["identifier"] != "ENG-1" {
		t.Errorf("resp identifier = %v, want ENG-1", resp["identifier"])
	}
}

func covIHUDelete(h *IssueHandler, userID, wsID, crewID, ident string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/crews/"+crewID+"/issues/"+ident, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestCovIHU_Delete_ExecError_500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `CREATE TRIGGER covihu_block_del BEFORE DELETE ON missions
		BEGIN SELECT RAISE(ABORT, 'covihu forced'); END`)

	rr := covIHUDelete(h, userID, wsID, crewID, "ENG-1")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovIHU_Delete_NonDeletableStatus_400(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	rr := covIHUDelete(h, userID, wsID, crewID, "ENG-1")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Only BACKLOG or CANCELLED") {
		t.Errorf("body = %s, want status guard message", rr.Body.String())
	}
}
