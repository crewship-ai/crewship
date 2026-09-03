package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// The S1 paging contract on GET /api/v1/issues: the body stays a bare array,
// the page obeys ?limit=&offset=, and X-Total-Count / X-Limit / X-Offset say
// how much the filter matched before the page was cut. The board used to
// print the length of the page it got ("100 issues" at 1 015).

func issueListRequest(t *testing.T, userID, wsID, query string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues"+query, nil)
	return withWorkspaceUser(req, userID, wsID, "OWNER")
}

func headerInt(t *testing.T, rr *httptest.ResponseRecorder, name string) int {
	t.Helper()
	v := rr.Header().Get(name)
	if v == "" {
		t.Fatalf("%s header missing; headers=%v", name, rr.Header())
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s = %q, not an int", name, v)
	}
	return n
}

func TestIssueList_PagesAndPublishesTotal(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	for i := 1; i <= 7; i++ {
		seedIssue(t, h.db, wsID, crewID, leadID, "ENG-"+strconv.Itoa(i), "BACKLOG")
	}

	rr := httptest.NewRecorder()
	h.List(rr, issueListRequest(t, userID, wsID, "?limit=3&offset=3"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var page []issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not a bare array: %v; body=%s", err, rr.Body.String())
	}
	if len(page) != 3 {
		t.Fatalf("page len = %d, want 3", len(page))
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 7 {
		t.Fatalf("X-Total-Count = %d, want 7 (the filter's total, not the page)", got)
	}
	if got := headerInt(t, rr, "X-Limit"); got != 3 {
		t.Fatalf("X-Limit = %d, want 3", got)
	}
	if got := headerInt(t, rr, "X-Offset"); got != 3 {
		t.Fatalf("X-Offset = %d, want 3", got)
	}
}

// A filter must reach the count too, or the header lies about the list.
func TestIssueList_TotalHonoursFilters(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "IN_PROGRESS")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-3", "IN_PROGRESS")

	rr := httptest.NewRecorder()
	h.List(rr, issueListRequest(t, userID, wsID, "?status=IN_PROGRESS&limit=1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 2 {
		t.Fatalf("X-Total-Count = %d, want 2 in-progress issues", got)
	}
}

// ?q= searches on the server, by title or identifier, so a client can find an
// issue that is not on the page it loaded.
func TestIssueList_SearchByIdentifierOrTitle(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	target := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-42", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET title = 'Rotate the staging deploy token' WHERE id = ?`, target); err != nil {
		t.Fatalf("retitle: %v", err)
	}
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	for _, q := range []string{"?q=ENG-42", "?q=deploy+token", "?search=deploy"} {
		rr := httptest.NewRecorder()
		h.List(rr, issueListRequest(t, userID, wsID, q))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", q, rr.Code, rr.Body.String())
		}
		var page []issueResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("%s: unmarshal: %v", q, err)
		}
		if len(page) != 1 || page[0].ID != target {
			t.Fatalf("%s: got %d rows (want the one matching issue); body=%s", q, len(page), rr.Body.String())
		}
		if got := headerInt(t, rr, "X-Total-Count"); got != 1 {
			t.Fatalf("%s: X-Total-Count = %d, want 1", q, got)
		}
	}
}

// An agent assignee carries its slug, so the board and the detail can link
// the agent instead of printing a name nobody can follow.
func TestIssueList_CarriesAssigneeSlug(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	if _, err := h.db.Exec(`UPDATE missions SET assignee_type = 'agent', assignee_id = ? WHERE id = ?`, workerID, id); err != nil {
		t.Fatalf("assign: %v", err)
	}
	rr := httptest.NewRecorder()
	h.List(rr, issueListRequest(t, userID, wsID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var page []issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(page) != 1 || page[0].AssigneeSlug == nil || *page[0].AssigneeSlug != "worker" {
		t.Fatalf("assignee_slug missing or wrong; body=%s", rr.Body.String())
	}
}
