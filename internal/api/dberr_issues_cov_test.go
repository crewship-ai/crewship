package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// These tests exercise the "Internal server error" (500) branches of the
// issue/project/mission family of handlers via fault injection: seed any
// rows needed to clear the 400/403 guards, build a fully-valid request,
// then close the DB IMMEDIATELY before invoking the handler so the first
// query inside the handler fails with "sql: database is closed".
//
// setupTestDB registers its own cleanup Close(); the explicit Close here
// is harmless (double close is a no-op for *sql.DB).

// covDBEClosedDB returns a workspace-scoped DB that is already closed, plus
// the seeded user/workspace IDs. The handler's first DB query will fail.
func covDBEClosedDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return db, userID, wsID
}

// covDBEAssert500 invokes fn against a recorder and asserts a 500 response.
func covDBEAssert500(t *testing.T, name string, req *http.Request, fn http.HandlerFunc) {
	t.Helper()
	rr := httptest.NewRecorder()
	fn(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("%s: expected 500, got %d (body: %s)", name, rr.Code, rr.Body.String())
	}
}

// ── mission_handler.go / mission_handler_mutate.go ─────────────────────────

func TestCovDBE_Mission(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewMissionHandler(db, nil, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/crews/c1/missions", nil),
			fn:   h.List,
		},
		{
			name: "Get",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/crews/c1/missions/m1", nil),
			fn:   h.Get,
		},
		{
			name: "Create",
			req: httptest.NewRequest(http.MethodPost, "/api/v1/crews/c1/missions",
				jsonBody(map[string]any{"title": "T", "lead_agent_id": "a1"})),
			fn: h.Create,
		},
		{
			name: "Update",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/crews/c1/missions/m1",
				jsonBody(map[string]any{"title": "T2"})),
			fn: h.Update,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/crews/c1/missions/m1", nil),
			fn:   h.Delete,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("crewId", "c1")
		tc.req.SetPathValue("missionId", "m1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "Mission."+tc.name, req, tc.fn)
	}
}

// ── project_handler.go ─────────────────────────────────────────────────────

func TestCovDBE_Project(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewProjectHandler(db, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil),
			fn:   h.List,
		},
		{
			name: "Create",
			req: httptest.NewRequest(http.MethodPost, "/api/v1/projects",
				jsonBody(map[string]any{"name": "P"})),
			fn: h.Create,
		},
		{
			name: "Get",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1", nil),
			fn:   h.Get,
		},
		{
			name: "Update",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/projects/p1",
				jsonBody(map[string]any{"name": "P2"})),
			fn: h.Update,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/projects/p1", nil),
			fn:   h.Delete,
		},
		{
			name: "Stats",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/stats", nil),
			fn:   h.Stats,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("projectId", "p1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "Project."+tc.name, req, tc.fn)
	}
}

// ── triage_handler.go ──────────────────────────────────────────────────────

func TestCovDBE_Triage(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewTriageHandler(db, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "ListRules",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/triage/rules", nil),
			fn:   h.ListRules,
		},
		{
			name: "CreateRule",
			req: httptest.NewRequest(http.MethodPost, "/api/v1/triage/rules",
				jsonBody(map[string]any{"name": "R", "pattern": "p", "match_type": "contains"})),
			fn: h.CreateRule,
		},
		{
			name: "UpdateRule",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/triage/rules/r1",
				jsonBody(map[string]any{"name": "R2"})),
			fn: h.UpdateRule,
		},
		{
			name: "DeleteRule",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/triage/rules/r1", nil),
			fn:   h.DeleteRule,
		},
		{
			name: "Process",
			req:  httptest.NewRequest(http.MethodPost, "/api/v1/triage/process", nil),
			fn:   h.Process,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("id", "r1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "Triage."+tc.name, req, tc.fn)
	}
}

// ── milestone_handler.go ───────────────────────────────────────────────────

func TestCovDBE_Milestone(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewMilestoneHandler(db, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/projects/p1/milestones", nil),
			fn:   h.List,
		},
		{
			name: "Create",
			req: httptest.NewRequest(http.MethodPost, "/api/v1/projects/p1/milestones",
				jsonBody(map[string]any{"name": "M"})),
			fn: h.Create,
		},
		{
			name: "Update",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/projects/p1/milestones/ms1",
				jsonBody(map[string]any{"name": "M2"})),
			fn: h.Update,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/projects/p1/milestones/ms1", nil),
			fn:   h.Delete,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("projectId", "p1")
		tc.req.SetPathValue("milestoneId", "ms1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "Milestone."+tc.name, req, tc.fn)
	}
}

// ── recurring_issue_handler.go ─────────────────────────────────────────────
//
// Create is omitted: with a closed DB its crew-existence check returns
// 400 ("Crew not found in workspace") before any 500-yielding query, so
// it cannot be driven to a 500 via this technique.

func TestCovDBE_RecurringIssue(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewRecurringIssueHandler(db, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/recurring-issues", nil),
			fn:   h.List,
		},
		{
			name: "Update",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/recurring-issues/ri1",
				jsonBody(map[string]any{"title": "T2"})),
			fn: h.Update,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/recurring-issues/ri1", nil),
			fn:   h.Delete,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("id", "ri1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "RecurringIssue."+tc.name, req, tc.fn)
	}
}

// ── saved_view_handler.go ──────────────────────────────────────────────────

func TestCovDBE_SavedView(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewSavedViewHandler(db, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/saved-views", nil),
			fn:   h.List,
		},
		{
			name: "Create",
			req: httptest.NewRequest(http.MethodPost, "/api/v1/saved-views",
				jsonBody(map[string]any{"name": "V", "filters_json": "{}"})),
			fn: h.Create,
		},
		{
			name: "Update",
			req: httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/v1",
				jsonBody(map[string]any{"name": "V2"})),
			fn: h.Update,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/saved-views/v1", nil),
			fn:   h.Delete,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("viewId", "v1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "SavedView."+tc.name, req, tc.fn)
	}
}

// ── notification_handler.go ────────────────────────────────────────────────

func TestCovDBE_Notification(t *testing.T) {
	db, userID, wsID := covDBEClosedDB(t)
	h := NewNotificationHandler(db, nil, newTestLogger())
	db.Close()

	cases := []struct {
		name string
		req  *http.Request
		fn   http.HandlerFunc
	}{
		{
			name: "List",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil),
			fn:   h.List,
		},
		{
			name: "MarkRead",
			req:  httptest.NewRequest(http.MethodPost, "/api/v1/notifications/n1/read", nil),
			fn:   h.MarkRead,
		},
		{
			name: "MarkAllRead",
			req:  httptest.NewRequest(http.MethodPost, "/api/v1/notifications/read-all", nil),
			fn:   h.MarkAllRead,
		},
		{
			name: "Delete",
			req:  httptest.NewRequest(http.MethodDelete, "/api/v1/notifications/n1", nil),
			fn:   h.Delete,
		},
		{
			name: "Count",
			req:  httptest.NewRequest(http.MethodGet, "/api/v1/notifications/count", nil),
			fn:   h.Count,
		},
	}
	for _, tc := range cases {
		tc.req.SetPathValue("id", "n1")
		req := withWorkspaceUser(tc.req, userID, wsID, "OWNER")
		covDBEAssert500(t, "Notification."+tc.name, req, tc.fn)
	}
}
