package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// issue_duplicate_completed_at_test.go covers a side-effect of making
// DUPLICATE a reachable issue status (internal/statuses/transitions.go):
// every place that sets completed_at off the new status gated on an
// explicit `newStatus == "DONE" || newStatus == "CANCELLED"` check.
// While DUPLICATE was unreachable that omission was harmless; once a
// transition can actually land an issue in DUPLICATE, the same three
// call sites need to stamp completed_at too, or every issue marked a
// duplicate ends up with completed_at permanently NULL -- directly
// contradicting the fix's own premise that DUPLICATE mirrors CANCELLED.
//
// Three independent call sites reach the DB with a dynamic status value
// and must all agree: the public single-issue PATCH (IssueHandler.Update),
// the public bulk PATCH (IssueHandler.BulkUpdate), and the internal/IPC
// status update used by agent tooling (InternalIssueHandler.UpdateStatus).
// That's the full set -- grepped for every `newStatus ==` / `req.Status ==`
// completed_at gate in internal/api and found exactly these three, all
// fixed inline rather than extracted to a shared list/constant.
//
// Considered generalizing internal/statuses' reachability invariant to
// "every terminal (no-outgoing-transitions) status must trigger
// completed_at" so this class of bug closes for good, not just for
// DUPLICATE. Rejected: it doesn't hold today. DONE and CANCELLED both DO
// set completed_at but are NOT graph sinks (DONE -> BACKLOG, CANCELLED ->
// BACKLOG/TODO reopen edges exist), while FAILED has outgoing edges too
// and deliberately does NOT set completed_at even though the frontend's
// issue-card.tsx TERMINAL_STATUSES treats it as terminal for display.
// "Must set completed_at" is a product decision about what counts as
// closed, not a topological property of the transition graph -- coding
// it as a graph-sink check would silently miss a future non-sink status
// that should set completed_at (exactly FAILED's shape) and would be a
// false positive against any future legitimate non-completing sink.

func TestIssueUpdate_StatusDuplicate_SetsCompletedAt(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	req := withWorkspaceUser(
		httptest.NewRequest("PATCH", "/api/v1/crews/"+crewID+"/issues/ENG-1", jsonBody(map[string]any{"status": "DUPLICATE"})),
		userID, wsID, "OWNER")
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}

	var completedAt *string
	if err := h.db.QueryRow(`SELECT completed_at FROM missions WHERE identifier = 'ENG-1'`).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completedAt == nil || *completedAt == "" {
		t.Errorf("completed_at empty after DUPLICATE transition (single-issue PATCH)")
	}
}

func TestIssueBulkUpdate_StatusDuplicate_SetsCompletedAt(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "TODO")

	body := bytes.NewBufferString(`{"ids":["` + id + `"],"updates":{"status":"DUPLICATE"}}`)
	req := httptest.NewRequest("POST", "/", body)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.BulkUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["updated"] != 1 {
		t.Fatalf("updated = %d, want 1 (body=%s)", resp["updated"], rr.Body.String())
	}

	var completedAt *string
	if err := h.db.QueryRow(`SELECT completed_at FROM missions WHERE identifier = 'ENG-1'`).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completedAt == nil || *completedAt == "" {
		t.Errorf("completed_at empty after DUPLICATE transition (bulk PATCH)")
	}
}

func TestInternalIssueUpdateStatus_StatusDuplicate_SetsCompletedAt(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "REVIEW")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"DUPLICATE","agent_id":"agent-worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var completedAt *string
	if err := h.db.QueryRow(`SELECT completed_at FROM missions WHERE identifier = 'ENG-1'`).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if completedAt == nil || *completedAt == "" {
		t.Errorf("completed_at empty after DUPLICATE transition (internal UpdateStatus)")
	}
}
