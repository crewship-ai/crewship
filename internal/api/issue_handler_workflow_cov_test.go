package api

// Coverage tests for issue_handler_workflow.go — Review/Start/Stop guard
// branches, the task-reset re-run path, description propagation, and
// ListActivity error/empty branches. Reuses newTestIssueHandler /
// seedIssue from issue_handler_test.go + issue_helpers_test.go.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covIWReq(userID, wsID, role, method, body, crewID, ident string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func TestCovIWReview_Guards(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-10", "REVIEW")

	t.Run("not found 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Review(rec, covIWReq(userID, wsID, "OWNER", "POST", `{"action":"approve"}`, crewID, "ENG-999"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("bad action 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Review(rec, covIWReq(userID, wsID, "OWNER", "POST", `{"action":"shrug"}`, crewID, "ENG-10"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}

// TestCovIWStart_TaskResetOnRerun seeds an issue WITH an existing task —
// Start must reset it to PENDING and bump the iteration counter instead of
// creating a duplicate.
func TestCovIWStart_TaskResetOnRerun(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-20", "TODO")
	if _, err := h.db.Exec(`UPDATE missions SET assignee_id = ?, assignee_type = 'agent' WHERE id = ?`, workerID, missionID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, status, task_order, depends_on, iteration, result_summary, created_at, updated_at)
		VALUES ('t-rerun', ?, ?, 'Old task', 'FAILED', 1, '[]', 1, 'failed previously', datetime('now'), datetime('now'))`, missionID, workerID); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Start(rec, covIWReq(userID, wsID, "OWNER", "POST", ``, crewID, "ENG-20"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var status string
	var iteration int
	if err := h.db.QueryRow(`SELECT status, iteration FROM mission_tasks WHERE id = 't-rerun'`).Scan(&status, &iteration); err != nil {
		t.Fatalf("query: %v", err)
	}
	if status != "PENDING" || iteration != 2 {
		t.Errorf("task = (%s, iter %d), want (PENDING, 2)", status, iteration)
	}
	var missionStatus string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, missionID).Scan(&missionStatus); err != nil {
		t.Fatalf("query mission: %v", err)
	}
	if missionStatus != "IN_PROGRESS" {
		t.Errorf("mission status = %q, want IN_PROGRESS", missionStatus)
	}
}

// TestCovIWStart_DescriptionPropagatesToTask covers the description.Valid
// branch — a fresh issue with a description and non-LEAD assignee gets a
// default task carrying that description.
func TestCovIWStart_DescriptionPropagatesToTask(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-21", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET assignee_id = ?, assignee_type = 'agent', description = 'fix the bug' WHERE id = ?`, workerID, missionID); err != nil {
		t.Fatalf("assign: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Start(rec, covIWReq(userID, wsID, "OWNER", "POST", ``, crewID, "ENG-21"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var desc string
	if err := h.db.QueryRow(`SELECT description FROM mission_tasks WHERE mission_id = ?`, missionID).Scan(&desc); err != nil {
		t.Fatalf("query task: %v", err)
	}
	if desc != "fix the bug" {
		t.Errorf("task description = %q", desc)
	}
}

func TestCovIWStart_ChatInsertError500(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-22", "TODO")
	if _, err := h.db.Exec(`UPDATE missions SET assignee_id = ?, assignee_type = 'agent' WHERE id = ?`, workerID, missionID); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := h.db.Exec(`DROP TABLE chats`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Start(rec, covIWReq(userID, wsID, "OWNER", "POST", ``, crewID, "ENG-22"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCovIWStartStop_RoleForbidden(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	calls := []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
	}{
		{"Review", h.Review},
		{"Start", h.Start},
		{"Stop", h.Stop},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			// VIEWER role + no capability row for this fake user id.
			req := covIWReq("viewer-nope", wsID, "VIEWER", "POST", `{"action":"approve"}`, crewID, "ENG-1")
			c.fn(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s: status = %d, want 403", c.name, rec.Code)
			}
		})
	}
	_ = userID
}

func TestCovIWListActivity_EmptyAndError(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
		seedIssue(t, h.db, wsID, crewID, leadID, "ENG-30", "BACKLOG")
		rec := httptest.NewRecorder()
		h.ListActivity(rec, covIWReq(userID, wsID, "MEMBER", "GET", ``, crewID, "ENG-30"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Errorf("body = %q, want []", rec.Body.String())
		}
	})

	t.Run("query error 500", func(t *testing.T) {
		h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
		seedIssue(t, h.db, wsID, crewID, leadID, "ENG-31", "BACKLOG")
		if _, err := h.db.Exec(`DROP TABLE mission_activity`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		h.ListActivity(rec, covIWReq(userID, wsID, "MEMBER", "GET", ``, crewID, "ENG-31"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}
