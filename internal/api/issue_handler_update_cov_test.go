package api

import (
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

// A failed label replacement must roll back the whole PATCH, including fields
// already written and their revision counters. Exercise DELETE and INSERT
// failures separately so a successful removal cannot hide a failed insertion.
func TestCovIHU_Update_LabelWriteFailures_Rollback(t *testing.T) {
	for _, operation := range []string{"DELETE", "INSERT"} {
		t.Run(operation, func(t *testing.T) {
			db := setupTestDB(t)
			userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
			h := NewIssueHandler(db, nil, nil, newTestLogger())
			missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
			execOrFatal(t, db, `INSERT INTO labels(id,workspace_id,name,color,created_at) VALUES('covihu-l1',?,'bug','#f00',datetime('now')),('covihu-l2',?,'infra','#0f0',datetime('now'))`, wsID, wsID)
			execOrFatal(t, db, `INSERT INTO mission_labels(mission_id,label_id) VALUES(?,'covihu-l1')`, missionID)
			var originalTitle string
			var originalRevision, originalAggregate int64
			if err := db.QueryRow(`SELECT title,revision,aggregate_revision FROM missions WHERE id=?`, missionID).Scan(&originalTitle, &originalRevision, &originalAggregate); err != nil {
				t.Fatal(err)
			}
			execOrFatal(t, db, `CREATE TRIGGER covihu_block_labels BEFORE `+operation+` ON mission_labels BEGIN SELECT RAISE(ABORT,'covihu forced'); END`)
			rr := covIHUPatch(h, userID, wsID, crewID, "ENG-1", map[string]any{"title": "Must roll back with labels", "labels": []string{"covihu-l2"}})
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d want500; body=%s", rr.Code, rr.Body.String())
			}
			var title, label string
			var revision, aggregate int64
			if err := db.QueryRow(`SELECT title,revision,aggregate_revision FROM missions WHERE id=?`, missionID).Scan(&title, &revision, &aggregate); err != nil {
				t.Fatal(err)
			}
			if title != originalTitle || revision != originalRevision || aggregate != originalAggregate {
				t.Fatalf("failed PATCH changed issue: title=%q revision=%d aggregate=%d; before=%q/%d/%d", title, revision, aggregate, originalTitle, originalRevision, originalAggregate)
			}
			var labels int
			if err := db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(label_id),'') FROM mission_labels WHERE mission_id=?`, missionID).Scan(&labels, &label); err != nil {
				t.Fatal(err)
			}
			if labels != 1 || label != "covihu-l1" {
				t.Fatalf("failed replacement changed labels: count=%d label=%s", labels, label)
			}
		})
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
