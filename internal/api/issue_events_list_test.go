package api

// issue_events_list_test.go — ListEvents (GET .../issues/{identifier}/events?
// after_seq=), PRD-ISSUES-AND-ROUTINES-2026 §14.1, work package B11 (#2368).
//
// These are RED-FIRST for the endpoint: before issue_events_list.go existed,
// IssueHandler had no ListEvents method at all, so every test in this file
// failed to compile. The seq-ordering and after_seq-cursor assertions are
// the actual behavioural proof; the 404/400 cases are the usual edges.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func eventsReq(userID, wsID, role, crewID, ident, query string) *http.Request {
	target := "/x"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest("GET", target, nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", ident)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, role)
	return req.WithContext(ctx)
}

func TestIssue_ListEvents_OrderedBySeq(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-40", "BACKLOG")

	// Three activity rows, out of insertion order relative to their
	// intended seq is not possible to force directly (Emit allocates seq
	// itself), but logActivity calls it three times in sequence so seq
	// should read back 1, 2, 3 in that order regardless of created_at
	// granularity — the property ListActivity (created_at DESC) cannot
	// prove at all.
	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "first")
	h.logActivity(context.Background(), missionID, "user", userID, "priority_changed", "second")
	h.logActivity(context.Background(), missionID, "user", userID, "assignee_changed", "third")

	rec := httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-40", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var resp issueEventsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(resp.Events), resp.Events)
	}
	wantDetails := []string{"first", "second", "third"}
	for i, ev := range resp.Events {
		if ev.Details == nil || *ev.Details != wantDetails[i] {
			t.Errorf("event %d details = %v, want %q", i, ev.Details, wantDetails[i])
		}
		if ev.Seq != i+1 {
			t.Errorf("event %d seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
	if resp.LatestSeq != 3 {
		t.Errorf("latest_seq = %d, want 3", resp.LatestSeq)
	}
	if resp.AfterSeq != 0 {
		t.Errorf("after_seq = %d, want 0", resp.AfterSeq)
	}
}

func TestIssue_ListEvents_AfterSeq_ReturnsOnlyNewer(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-41", "BACKLOG")

	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "first")
	h.logActivity(context.Background(), missionID, "user", userID, "priority_changed", "second")
	h.logActivity(context.Background(), missionID, "user", userID, "assignee_changed", "third")

	// This is the gap-resync shape: a client that has already consumed
	// seq 1 asks for everything after it.
	rec := httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-41", "after_seq=1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp issueEventsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("got %d events, want 2 (seq 2 and 3 only): %+v", len(resp.Events), resp.Events)
	}
	if resp.Events[0].Seq != 2 || resp.Events[1].Seq != 3 {
		t.Errorf("seqs = [%d, %d], want [2, 3]", resp.Events[0].Seq, resp.Events[1].Seq)
	}
	if resp.AfterSeq != 1 {
		t.Errorf("after_seq echoed = %d, want 1", resp.AfterSeq)
	}

	// Fully caught up: after_seq at the high-water mark returns nothing,
	// and latest_seq still tells the caller where the mark is — this is
	// what lets a client's resync poll distinguish "nothing new" from
	// "server has no memory of this issue at all".
	rec2 := httptest.NewRecorder()
	h.ListEvents(rec2, eventsReq(userID, wsID, "OWNER", crewID, "ENG-41", "after_seq=3"))
	var resp2 issueEventsResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp2.Events) != 0 {
		t.Errorf("got %d events at the high-water mark, want 0", len(resp2.Events))
	}
	if resp2.LatestSeq != 3 {
		t.Errorf("latest_seq = %d, want 3", resp2.LatestSeq)
	}
}

func TestIssue_ListEvents_NotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)
	rec := httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-999", ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestIssue_ListEvents_InvalidAfterSeq_400(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-42", "BACKLOG")

	for _, bad := range []string{"after_seq=-1", "after_seq=nope"} {
		rec := httptest.NewRecorder()
		h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-42", bad))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400, body=%s", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestIssue_ListEvents_EmptyIssue(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-43", "BACKLOG")

	rec := httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-43", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("body = %s, want an empty events array", rec.Body.String())
	}
}

// #2983: Activity reads an issue's history newest first and pages back with
// "Load older". after_seq only walks forward from the start, so the reader
// had to download every event to show the last ten. before_seq + limit
// returns the newest `limit` events below the cursor, still in seq order.
func TestIssue_ListEvents_BeforeSeqPagesBackwards(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-50", "BACKLOG")
	for _, d := range []string{"e1", "e2", "e3", "e4", "e5"} {
		h.logActivity(context.Background(), missionID, "user", userID, "status_changed", d)
	}

	page := func(query string) issueEventsResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-50", query))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body=%s", query, rec.Code, rec.Body.String())
		}
		var resp issueEventsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}
	seqs := func(r issueEventsResponse) []int {
		out := []int{}
		for _, e := range r.Events {
			out = append(out, e.Seq)
		}
		return out
	}

	// The newest page: no cursor, only a limit.
	first := page("limit=2&before_seq=0")
	if got := seqs(first); len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("newest page = %v, want [4 5]", got)
	}
	if !first.HasOlder {
		t.Errorf("has_older = false on the newest page of five events")
	}
	second := page("limit=2&before_seq=4")
	if got := seqs(second); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("second page = %v, want [2 3]", got)
	}
	last := page("limit=2&before_seq=2")
	if got := seqs(last); len(got) != 1 || got[0] != 1 {
		t.Fatalf("last page = %v, want [1]", got)
	}
	if last.HasOlder {
		t.Errorf("has_older = true on the oldest page")
	}
	if last.LatestSeq != 5 {
		t.Errorf("latest_seq = %d, want 5", last.LatestSeq)
	}
}

func TestIssue_ListEvents_BeforeSeqRejectsBadInput(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-51", "BACKLOG")
	for _, q := range []string{"before_seq=-1", "before_seq=x", "limit=0&before_seq=0", "limit=abc&before_seq=0", "before_seq=3&after_seq=1"} {
		rec := httptest.NewRecorder()
		h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, "ENG-51", q))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

// Activity knows an issue by its mission id; a workspace without identifiers
// has nothing else. The crew-scoped sub-resources resolve either, fenced to
// the crew and workspace like the identifier is.
func TestIssue_ListEvents_ResolvesTheMissionID(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-52", "BACKLOG")
	h.logActivity(context.Background(), missionID, "user", userID, "status_changed", "only")

	rec := httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, wsID, "OWNER", crewID, missionID, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ListEvents(rec, eventsReq(userID, "ws_other", "OWNER", crewID, missionID, ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("another workspace resolved the mission id: status = %d, want 404", rec.Code)
	}
}

// #2983: the workspace-wide issue read also resolves the mission id, so a page
// that knows an issue only by id (Activity, a workspace without identifiers)
// can learn its crew and identifier — and nothing of another workspace's.
func TestIssue_GetByIdentifier_ResolvesTheMissionID(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	missionID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-53", "BACKLOG")

	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"identifier": missionID})
	h.GetByIdentifier(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		CrewID     string `json:"crew_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != missionID || got.Identifier != "ENG-53" || got.CrewID != crewID {
		t.Errorf("got %+v, want id %s, ENG-53, crew %s", got, missionID, crewID)
	}

	req, rr = covIHReq("GET", "", userID, "ws_other", "OWNER", map[string]string{"identifier": missionID})
	h.GetByIdentifier(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("another workspace resolved the mission id: status = %d, want 404", rr.Code)
	}
}
