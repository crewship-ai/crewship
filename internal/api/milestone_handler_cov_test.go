package api

// Coverage tests for milestone_handler.go — empty list, list/create DB
// error branches, and Create body validation. Reuses milestoneRig-style
// seeding from project_milestone_test.go conventions.

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covMSRig(t *testing.T) (*MilestoneHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	pid := "proj-ms"
	if _, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug, created_at, updated_at)
		VALUES (?, ?, 'P', 'p-ms', datetime('now'), datetime('now'))`, pid, wsID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return NewMilestoneHandler(db, nil, newTestLogger()), db, userID, wsID, pid
}

func covMSReq(userID, wsID, role, method, body, projectID, milestoneID string) *http.Request {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	if projectID != "" {
		req.SetPathValue("projectId", projectID)
	}
	if milestoneID != "" {
		req.SetPathValue("milestoneId", milestoneID)
	}
	return withWorkspaceUser(req, userID, wsID, role)
}

func TestCovMSList_Empty(t *testing.T) {
	h, _, userID, wsID, pid := covMSRig(t)
	rec := httptest.NewRecorder()
	h.List(rec, covMSReq(userID, wsID, "MEMBER", "GET", "", pid, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q, want []", rec.Body.String())
	}
}

func TestCovMSList_QueryError500(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)
	// Project check passes; milestones query fails.
	if _, err := db.Exec(`DROP TABLE milestones`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.List(rec, covMSReq(userID, wsID, "MEMBER", "GET", "", pid, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovMSCreate_Validation(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)

	t.Run("invalid JSON 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Create(rec, covMSReq(userID, wsID, "ADMIN", "POST", `{bad`, pid, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("missing name 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Create(rec, covMSReq(userID, wsID, "ADMIN", "POST", `{}`, pid, ""))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("default status active", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Create(rec, covMSReq(userID, wsID, "ADMIN", "POST", `{"name":"M1"}`, pid, ""))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM milestones WHERE project_id = ? AND name = 'M1'`, pid).Scan(&status); err != nil {
			t.Fatalf("query: %v", err)
		}
		if status != "active" {
			t.Errorf("status = %q, want active", status)
		}
	})
}

func TestCovMSCreate_InsertError500(t *testing.T) {
	h, db, userID, wsID, pid := covMSRig(t)
	// Project check passes; max-position lookup logs and INSERT fails.
	if _, err := db.Exec(`DROP TABLE milestones`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	rec := httptest.NewRecorder()
	h.Create(rec, covMSReq(userID, wsID, "ADMIN", "POST", `{"name":"M2"}`, pid, ""))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
