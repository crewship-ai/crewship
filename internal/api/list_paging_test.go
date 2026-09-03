package api

// #2303 — the windowed list endpoints describe their window in headers.
// Before this, GET /crews on a 103-crew workspace answered 100 rows and
// nothing on the wire said there were more.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func assertPaging(t *testing.T, rr *httptest.ResponseRecorder, total, limit, offset int) {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	for name, want := range map[string]int{"X-Total-Count": total, "X-Limit": limit, "X-Offset": offset} {
		if got := rr.Header().Get(name); got != fmt.Sprint(want) {
			t.Errorf("%s = %q, want %d", name, got, want)
		}
	}
}

func bodyLen(t *testing.T, rr *httptest.ResponseRecorder) int {
	t.Helper()
	var raw []json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not a bare array: %v (%s)", err, rr.Body.String())
	}
	return len(raw)
}

func TestCrewList_PagingHeaders(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("crew-%d", i)
		seedCrewRow(t, db, id, wsID, "Crew "+id, id)
		if _, err := db.Exec(`UPDATE crews SET created_at = ? WHERE id = ?`, fmt.Sprintf("2026-01-01T00:00:0%dZ", i), id); err != nil {
			t.Fatalf("stamp %s: %v", id, err)
		}
	}
	// The onboarding setup crew is excluded from the page, so it must be
	// excluded from the total too — otherwise "3 of 4" with no fourth row.
	seedCrewRowKind(t, db, "crew-setup", wsID, "Guide", "_crewship-setup-guide", "setup")
	// A soft-deleted crew is neither.
	seedCrewRow(t, db, "crew-gone", wsID, "Gone", "gone")
	if _, err := db.Exec(`UPDATE crews SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 'crew-gone'`); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	h := NewCrewHandler(db, newTestLogger())
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/crews"+query, nil)
		req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.List(rr, req)
		return rr
	}

	rr := get("")
	assertPaging(t, rr, 3, 100, 0)
	if n := bodyLen(t, rr); n != 3 {
		t.Errorf("default page has %d rows, want 3", n)
	}

	rr = get("?limit=2")
	assertPaging(t, rr, 3, 2, 0)
	if n := bodyLen(t, rr); n != 2 {
		t.Errorf("limit=2 page has %d rows, want 2", n)
	}

	rr = get("?limit=2&offset=2")
	assertPaging(t, rr, 3, 2, 2)
	if n := bodyLen(t, rr); n != 1 {
		t.Errorf("limit=2&offset=2 page has %d rows, want 1", n)
	}

	// ?q= searches name and slug on the server, case-insensitively, and
	// narrows the total with the page.
	rr = get("?q=CREW-2")
	assertPaging(t, rr, 1, 100, 0)
	if n := bodyLen(t, rr); n != 1 {
		t.Errorf("q=CREW-2 page has %d rows, want 1", n)
	}
	rr = get("?q=zzzz")
	assertPaging(t, rr, 0, 100, 0)
	if n := bodyLen(t, rr); n != 0 {
		t.Errorf("q=zzzz page has %d rows, want 0", n)
	}
}

func TestAgentList_PagingHeaders(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-a", wsID, "A", "a")
	seedCrewRow(t, db, "crew-b", wsID, "B", "b")
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("ag-a-%d", i)
		seedAgentRow(t, db, id, wsID, "crew-a", id, id, "AGENT")
	}
	seedAgentRow(t, db, "ag-b-1", wsID, "crew-b", "ag-b-1", "ag-b-1", "AGENT")
	seedAgentRow(t, db, "ag-gone", wsID, "crew-b", "ag-gone", "ag-gone", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 'ag-gone'`); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}

	h := NewAgentHandler(db, newTestLogger())
	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/agents"+query, nil)
		req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
		rr := httptest.NewRecorder()
		h.List(rr, req)
		return rr
	}

	rr := get("?limit=2")
	assertPaging(t, rr, 4, 2, 0)
	if n := bodyLen(t, rr); n != 2 {
		t.Errorf("limit=2 page has %d rows, want 2", n)
	}

	// The crew filter narrows the total as well as the page.
	rr = get("?crew_id=crew-a&limit=1&offset=1")
	assertPaging(t, rr, 3, 1, 1)
	if n := bodyLen(t, rr); n != 1 {
		t.Errorf("crew page has %d rows, want 1", n)
	}

	// ?q= matches name, slug or role title; combined with the crew filter.
	rr = get("?q=AG-A-2")
	assertPaging(t, rr, 1, 100, 0)
	rr = get("?q=ag-&crew_id=crew-b")
	assertPaging(t, rr, 1, 100, 0)
}

func TestCredentialList_PagingHeaders(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	for i := 1; i <= 3; i++ {
		seedCredWithMeta(t, h, wsID, userID, fmt.Sprintf("c%d", i), fmt.Sprintf("CRED_%d", i), fmt.Sprintf("2026-01-01 00:00:0%d", i), "")
	}

	// Offset window: the bare array stays, the headers describe it.
	rr := httptest.NewRecorder()
	h.List(rr, listReq(wsID, "limit=2&offset=1"))
	assertPaging(t, rr, 3, 2, 1)
	if n := bodyLen(t, rr); n != 2 {
		t.Errorf("limit=2&offset=1 page has %d rows, want 2", n)
	}

	// A server-side search narrows the total too; q and the older search
	// spelling are the same filter.
	rr = httptest.NewRecorder()
	h.List(rr, listReq(wsID, "search=CRED_1"))
	assertPaging(t, rr, 1, 100, 0)
	rr = httptest.NewRecorder()
	h.List(rr, listReq(wsID, "q=CRED_1"))
	assertPaging(t, rr, 1, 100, 0)

	// The cursor envelope carries the total and page size as well; there is
	// no offset on a cursor page, so that header stays absent.
	rr = httptest.NewRecorder()
	h.List(rr, listReq(wsID, "paginate=true&limit=2"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Total-Count"); got != "3" {
		t.Errorf("X-Total-Count = %q on the cursor path, want 3", got)
	}
	if got := rr.Header().Get("X-Limit"); got != "2" {
		t.Errorf("X-Limit = %q on the cursor path, want 2", got)
	}
	if got := rr.Header().Get("X-Offset"); got != "" {
		t.Errorf("X-Offset = %q on the cursor path, want it absent", got)
	}
}
