package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression tests for #2426: enum values the projects table's CHECK
// constraints reject used to surface as a 500 from the project
// handler, and the issue create handler ignored `status` entirely.

func TestProjectCreate_EnumValidation(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantCode  int
		wantField string // must appear in a 400 body
	}{
		{"status active is the manifest's old word", map[string]any{"name": "P", "status": "active"}, http.StatusBadRequest, "status"},
		{"status archived", map[string]any{"name": "P", "status": "archived"}, http.StatusBadRequest, "status"},
		{"priority critical", map[string]any{"name": "P", "priority": "critical"}, http.StatusBadRequest, "priority"},
		{"status in_progress", map[string]any{"name": "P", "status": "in_progress"}, http.StatusCreated, ""},
		{"status paused priority urgent", map[string]any{"name": "P", "status": "paused", "priority": "urgent"}, http.StatusCreated, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := covProjHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(tc.body))
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rec := httptest.NewRecorder()
			h.Create(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantField != "" && !strings.Contains(rec.Body.String(), tc.wantField) {
				t.Fatalf("400 body should name %q, got %s", tc.wantField, rec.Body.String())
			}
		})
	}
}

func TestProjectUpdate_EnumValidation(t *testing.T) {
	cases := []struct {
		name      string
		body      map[string]any
		wantCode  int
		wantField string
	}{
		{"status active", map[string]any{"status": "active"}, http.StatusBadRequest, "status"},
		{"health great", map[string]any{"health": "great"}, http.StatusBadRequest, "health"},
		{"priority critical", map[string]any{"priority": "critical"}, http.StatusBadRequest, "priority"},
		{"health at_risk", map[string]any{"health": "at_risk"}, http.StatusOK, ""},
		{"status cancelled", map[string]any{"status": "cancelled"}, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := covProjHandler(t)
			pid := seedProject(t, h.db, wsID, "P")
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+pid, jsonBody(tc.body))
			req.SetPathValue("projectId", pid)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rec := httptest.NewRecorder()
			h.Update(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantField != "" && !strings.Contains(rec.Body.String(), tc.wantField) {
				t.Fatalf("400 body should name %q, got %s", tc.wantField, rec.Body.String())
			}
		})
	}
}

// TestIssueCreate_Status: the create body may carry a starting status.
// It is validated the way an update from the default BACKLOG would be,
// so anything that is not one step from BACKLOG is refused with a 400.
func TestIssueCreate_Status(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantCode   int
		wantStatus string
	}{
		{"omitted defaults to BACKLOG", `{"title":"t"}`, http.StatusCreated, "BACKLOG"},
		{"TODO", `{"title":"t","status":"TODO"}`, http.StatusCreated, "TODO"},
		{"IN_PROGRESS", `{"title":"t","status":"IN_PROGRESS"}`, http.StatusCreated, "IN_PROGRESS"},
		{"explicit BACKLOG", `{"title":"t","status":"BACKLOG"}`, http.StatusCreated, "BACKLOG"},
		{"DONE is not a starting status", `{"title":"t","status":"DONE"}`, http.StatusBadRequest, ""},
		{"lowercase is not canonical", `{"title":"t","status":"todo"}`, http.StatusBadRequest, ""},
		{"garbage", `{"title":"t","status":"nope"}`, http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, userID, wsID, crewID := covICHandler(t)
			rr := covICPost(t, h, userID, wsID, crewID, tc.body)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusCreated {
				if !strings.Contains(rr.Body.String(), "status") {
					t.Fatalf("400 body should name the status field, got %s", rr.Body.String())
				}
				return
			}
			var resp issueResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Status != tc.wantStatus {
				t.Errorf("response status = %q, want %q", resp.Status, tc.wantStatus)
			}
			var stored string
			if err := h.db.QueryRow(`SELECT status FROM missions WHERE id = ?`, resp.ID).Scan(&stored); err != nil {
				t.Fatalf("read row: %v", err)
			}
			if stored != tc.wantStatus {
				t.Errorf("stored status = %q, want %q", stored, tc.wantStatus)
			}
		})
	}
}
