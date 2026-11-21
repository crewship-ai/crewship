package api

// #5603 — GET /api/v1/crews/{crewId}/assignments filters by assignee and
// status in SQL, before LIMIT/OFFSET, and publishes the filtered total in
// X-Total-Count. The Chat panel's "Now" section relied on reading the crew's
// 100 newest rows and filtering in the browser, which loses an older QUEUED
// row in a busy crew; these tests pin the server half of that fix.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const (
	filterWS      = "test-workspace-id"
	filterOtherWS = "ws-foreign"
)

// seedAssignmentFilterFixture builds:
//
//	crew1 (ws A): lead, worker, helper
//	crew2 (ws A): other
//	crew3 (ws B): a foreign agent whose slug is "worker" too — agent slugs
//	              are unique per workspace only, so a slug lookup that is not
//	              pinned to the caller's workspace would match it.
//
// worker has 5 QUEUED + 2 PENDING + 3 COMPLETED rows in crew1, helper has
// 2 QUEUED, other has 4 QUEUED in crew2, and the foreign worker has 3 QUEUED
// in ws B. One corrupt ws-A row points at the foreign agent, so the
// workspace pin on the assignee is exercised on its own.
func seedAssignmentFilterFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	userID := seedTestUser(t, db)
	seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign')`, filterOtherWS)
	for _, c := range [][3]string{{"crew1", filterWS, "alpha"}, {"crew2", filterWS, "beta"}, {"crew3", filterOtherWS, "gamma"}} {
		execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, c[0], c[1], c[2], c[2])
	}
	for _, a := range [][4]string{
		{"ag-lead", "crew1", filterWS, "lead"},
		{"ag-worker", "crew1", filterWS, "worker"},
		{"ag-helper", "crew1", filterWS, "helper"},
		{"ag-other", "crew2", filterWS, "other"},
		{"ag-foreign", "crew3", filterOtherWS, "worker"},
	} {
		execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, ?, ?)`, a[0], a[1], a[2], a[3], a[3])
	}
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat-a', 'ag-lead', ?, 'CHAT', 'ACTIVE')`, filterWS)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat-b', 'ag-other', ?, 'CHAT', 'ACTIVE')`, filterWS)
	execOrFatal(t, db, `INSERT INTO chats (id, agent_id, workspace_id, mode, status) VALUES ('chat-c', 'ag-foreign', ?, 'CHAT', 'ACTIVE')`, filterOtherWS)

	n := 0
	add := func(ws, chat, by, to, status string, count int) {
		for i := 0; i < count; i++ {
			n++
			execOrFatal(t, db,
				`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				fmt.Sprintf("asg-%02d", n), ws, chat, by, to, fmt.Sprintf("%s %s %d", to, status, i), status,
				fmt.Sprintf("2026-01-01T00:%02d:00Z", n))
		}
	}
	add(filterWS, "chat-a", "ag-lead", "ag-worker", "QUEUED", 5)
	add(filterWS, "chat-a", "ag-lead", "ag-worker", "PENDING", 2)
	add(filterWS, "chat-a", "ag-lead", "ag-worker", "COMPLETED", 3)
	add(filterWS, "chat-a", "ag-lead", "ag-helper", "QUEUED", 2)
	add(filterWS, "chat-b", "ag-other", "ag-other", "QUEUED", 4)
	// This crew's lead delegates to an agent of another crew: the row is in
	// crew1's list (assigned_by is crew1), so ?assigned_to= finds it there.
	add(filterWS, "chat-a", "ag-lead", "ag-other", "QUEUED", 1)
	add(filterOtherWS, "chat-c", "ag-foreign", "ag-foreign", "QUEUED", 3)
	// Corrupt row: in ws A, dispatched by crew1's lead, assigned to the
	// foreign workspace's agent. The crew list shows it unfiltered (it is
	// this crew's dispatch); the assignee filter must not resolve to it.
	add(filterWS, "chat-a", "ag-lead", "ag-foreign", "QUEUED", 1)
}

func listCrewAssignments(t *testing.T, h *AssignmentHandler, crewID, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/crews/"+crewID+"/assignments?"+query, nil)
	req.SetPathValue("crewId", crewID)
	req = req.WithContext(context.WithValue(req.Context(), ctxWorkspaceID, filterWS))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	return rr
}

func decodeAssignmentRows(t *testing.T, rr *httptest.ResponseRecorder) []assignmentListItem {
	t.Helper()
	var rows []assignmentListItem
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	return rows
}

func TestAssignmentList_Filters(t *testing.T) {
	db := setupTestDB(t)
	seedAssignmentFilterFixture(t, db)
	h := NewAssignmentHandler(db, nil, nil, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))

	cases := []struct {
		name     string
		crew     string
		query    url.Values
		total    int
		page     int
		to       string   // every row's assigned_to_slug, when set
		statuses []string // every row's status is one of these, when set
	}{
		{name: "unfiltered crew list keeps its old scope", crew: "crew1", query: url.Values{}, total: 14, page: 14},
		{name: "assignee by id", crew: "crew1", query: url.Values{"assigned_to": {"ag-worker"}}, total: 10, page: 10, to: "worker"},
		{name: "assignee by slug", crew: "crew1", query: url.Values{"assigned_to": {"worker"}}, total: 10, page: 10, to: "worker"},
		{name: "single status", crew: "crew1", query: url.Values{"status": {"QUEUED"}}, total: 9, page: 9, statuses: []string{"QUEUED"}},
		{name: "multiple statuses with assignee", crew: "crew1", query: url.Values{"assigned_to": {"worker"}, "status": {"PENDING,QUEUED"}}, total: 7, page: 7, to: "worker", statuses: []string{"PENDING", "QUEUED"}},
		{name: "status is case and space tolerant", crew: "crew1", query: url.Values{"assigned_to": {"worker"}, "status": {" pending , Queued ,"}}, total: 7, page: 7, statuses: []string{"PENDING", "QUEUED"}},
		{name: "total counts the filter, page is the window", crew: "crew1", query: url.Values{"assigned_to": {"worker"}, "status": {"QUEUED,PENDING"}, "limit": {"3"}, "offset": {"3"}}, total: 7, page: 3, to: "worker"},
		{name: "last page is short, total unchanged", crew: "crew1", query: url.Values{"assigned_to": {"worker"}, "status": {"QUEUED,PENDING"}, "limit": {"3"}, "offset": {"6"}}, total: 7, page: 1, to: "worker"},
		{name: "unknown agent is an empty page, not a 404", crew: "crew1", query: url.Values{"assigned_to": {"nobody"}}, total: 0, page: 0},
		{name: "agent from another crew matches only the work this crew gave it", crew: "crew1", query: url.Values{"assigned_to": {"other"}}, total: 1, page: 1, to: "other"},
		{name: "foreign workspace agent id matches nothing", crew: "crew1", query: url.Values{"assigned_to": {"ag-foreign"}}, total: 0, page: 0},
		{name: "foreign workspace crew is not readable", crew: "crew3", query: url.Values{"assigned_to": {"worker"}}, total: 0, page: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := listCrewAssignments(t, h, tc.crew, tc.query.Encode())
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("X-Total-Count"); got != fmt.Sprint(tc.total) {
				t.Errorf("X-Total-Count = %q, want %d", got, tc.total)
			}
			rows := decodeAssignmentRows(t, rr)
			if len(rows) != tc.page {
				t.Fatalf("page = %d rows, want %d: %+v", len(rows), tc.page, rows)
			}
			for _, row := range rows {
				if tc.to != "" && row.AssignedToSlug != tc.to {
					t.Errorf("row %s assigned to %q, want %q", row.ID, row.AssignedToSlug, tc.to)
				}
				if tc.statuses != nil && !slices.Contains(tc.statuses, row.Status) {
					t.Errorf("row %s status %q, want one of %v", row.ID, row.Status, tc.statuses)
				}
			}
		})
	}
}

// The oldest QUEUED row must be reachable even when far more than one page
// of newer rows exists — the exact case the client-side filter lost.
func TestAssignmentList_FilterRunsBeforeLimit(t *testing.T) {
	db := setupTestDB(t)
	seedAssignmentFilterFixture(t, db)
	// 120 newer COMPLETED rows for worker push every QUEUED row out of the
	// unfiltered 100-row window.
	for i := 0; i < 120; i++ {
		execOrFatal(t, db,
			`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
			 VALUES (?, ?, 'chat-a', 'ag-lead', 'ag-worker', 'done', 'COMPLETED', ?)`,
			fmt.Sprintf("newer-%03d", i), filterWS, fmt.Sprintf("2026-02-01T%02d:%02d:00Z", i/60, i%60))
	}
	h := NewAssignmentHandler(db, nil, nil, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))

	unfiltered := decodeAssignmentRows(t, listCrewAssignments(t, h, "crew1", "limit=100"))
	for _, row := range unfiltered {
		if row.Status == "QUEUED" && row.AssignedToSlug == "worker" {
			t.Fatalf("fixture too small: a queued worker row fits in the unfiltered window")
		}
	}
	rr := listCrewAssignments(t, h, "crew1", "limit=100&assigned_to=worker&status=QUEUED,PENDING")
	if got := rr.Header().Get("X-Total-Count"); got != "7" {
		t.Errorf("X-Total-Count = %q, want 7", got)
	}
	if rows := decodeAssignmentRows(t, rr); len(rows) != 7 {
		t.Errorf("filtered page = %d rows, want 7", len(rows))
	}
}

func TestAssignmentList_RejectsUnknownStatus(t *testing.T) {
	db := setupTestDB(t)
	seedAssignmentFilterFixture(t, db)
	h := NewAssignmentHandler(db, nil, nil, "token", slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, status := range []string{"QUEUD", "QUEUED,DONE", "running;drop"} {
		rr := listCrewAssignments(t, h, "crew1", url.Values{"status": {status}}.Encode())
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status=%q: code = %d, want 400 (%s)", status, rr.Code, rr.Body.String())
		}
		// The file's envelope: {"error": "..."} naming the accepted values.
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("status=%q: body is not the error envelope: %s", status, rr.Body.String())
		}
		for _, want := range assignmentStatuses {
			if !strings.Contains(body["error"], want) {
				t.Errorf("status=%q: error %q does not name %s", status, body["error"], want)
			}
		}
		if rr.Header().Get("X-Total-Count") != "" {
			t.Errorf("status=%q: a rejected request must not publish a total", status)
		}
	}
}

func TestParseAssignmentStatuses(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
		ok   bool
	}{
		{"QUEUED", []string{"QUEUED"}, true},
		{"queued,PENDING", []string{"QUEUED", "PENDING"}, true},
		{"QUEUED,QUEUED", []string{"QUEUED"}, true},
		{",", nil, true},
		{"QUEUED,nope", nil, false},
		{"TIMEOUT", nil, false},
	}
	for _, tc := range cases {
		got, ok := parseAssignmentStatuses(tc.raw)
		if ok != tc.ok || !slices.Equal(got, tc.want) {
			t.Errorf("parseAssignmentStatuses(%q) = %v, %v; want %v, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}
