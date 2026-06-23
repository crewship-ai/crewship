package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// issues_internal_cov2_test.go — second pass over InternalIssueHandler:
// Create's DB/write failure arms, the warn-only label/comment write
// failures, UpdateStatus's guards + write failure, and CreateComment's
// failure arms. Helpers prefixed covII3.

func covII3Create(h *InternalIssueHandler, wsID, crewID string, extra map[string]any) *httptest.ResponseRecorder {
	body := map[string]any{
		"workspace_id": wsID, "crew_id": crewID, "title": "Internal issue",
	}
	for k, v := range extra {
		body[k] = v
	}
	req := httptest.NewRequest("POST", "/api/v1/internal/issues", jsonBody(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestCovII3_Create_AuthorValidateDBError_500(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	db.Close()
	rr := covII3Create(h, wsID, crewID, map[string]any{"author_agent_id": leadID})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_Create_CrewLookupDBError_500(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	execOrFatal(t, db, `ALTER TABLE crews RENAME TO crews_broken`)
	rr := covII3Create(h, wsID, crewID, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_Create_CounterFailure_500(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_ctr BEFORE INSERT ON issue_counters
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	rr := covII3Create(h, wsID, crewID, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_Create_MissionInsertFailure_500(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_mission BEFORE INSERT ON missions
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	rr := covII3Create(h, wsID, crewID, nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovII3_Create_LabelInsertFailure_Warn_201 — a blocked label link
// is logged but the issue creation still succeeds.
func TestCovII3_Create_LabelInsertFailure_Warn_201(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	execOrFatal(t, db, `INSERT INTO labels (id, workspace_id, name, color, created_at)
		VALUES ('covii3-l1', ?, 'bug', '#f00', datetime('now'))`, wsID)
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_ml BEFORE INSERT ON mission_labels
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	rr := covII3Create(h, wsID, crewID, map[string]any{"labels": []string{"covii3-l1"}})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (label link is best-effort); body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestCovII3_Get_CommentCountFailure_Warn_200 — the Get response
// survives a broken mission_comments table (count is best-effort).
func TestCovII3_Get_CommentCountFailure_Warn_200(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `ALTER TABLE mission_comments RENAME TO mc_broken`)
	req := httptest.NewRequest("GET", "/api/v1/internal/issues/ENG-1?workspace_id="+wsID, nil)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_UpdateStatus_MissingWorkspaceID_400(t *testing.T) {
	h, _, _, _, _ := covII2NewIssueHandler(t)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/issues/ENG-1/status",
		jsonBody(map[string]any{"status": "IN_PROGRESS"}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_UpdateStatus_ExecError_500(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_upd BEFORE UPDATE ON missions
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/issues/ENG-1/status",
		jsonBody(map[string]any{"workspace_id": wsID, "status": "IN_PROGRESS"}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovII3_UpdateStatus_CommentInsertFailure_Warn_200 — the status
// flip lands, the optional comment write fails silently.
func TestCovII3_UpdateStatus_CommentInsertFailure_Warn_200(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_mc BEFORE INSERT ON mission_comments
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	req := httptest.NewRequest("PATCH", "/api/v1/internal/issues/ENG-1/status",
		jsonBody(map[string]any{
			"workspace_id": wsID, "status": "IN_PROGRESS", "comment": "starting work",
		}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if n := covII2CommentCount(t, db, missionID); n != 0 {
		t.Errorf("comments = %d, want 0 (insert blocked)", n)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status); err != nil || status != "IN_PROGRESS" {
		t.Errorf("status = %q err=%v, want IN_PROGRESS", status, err)
	}
}

func TestCovII3_CreateComment_MissingFields_400(t *testing.T) {
	h, _, _, _, _ := covII2NewIssueHandler(t)
	req := httptest.NewRequest("POST", "/api/v1/internal/issues/ENG-1/comments",
		jsonBody(map[string]any{"body": ""}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_CreateComment_FindIssueDBError_500(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	db.Close()
	req := httptest.NewRequest("POST", "/api/v1/internal/issues/ENG-1/comments",
		jsonBody(map[string]any{"workspace_id": wsID, "body": "hi"}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovII3_CreateComment_InsertFailure_500(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	execOrFatal(t, db, `CREATE TRIGGER covii3_block_mc2 BEFORE INSERT ON mission_comments
		BEGIN SELECT RAISE(ABORT, 'covii3 forced'); END`)
	req := httptest.NewRequest("POST", "/api/v1/internal/issues/ENG-1/comments",
		jsonBody(map[string]any{"workspace_id": wsID, "body": "hi"}))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
