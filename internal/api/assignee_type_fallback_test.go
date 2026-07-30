package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file pins the assignee_type fallback fix in issue_handler_update.go's
// Update, issue_handler_bulk.go's BulkUpdate, and recurring_issue_handler.go's
// Update.
//
// Repro (found by adversarial review of the assignee_write_invariant_test.go
// PR, not by an existing test — none of the three exercised this branch):
// an issue currently has assignee_type='user'. A client PATCHes only
// assignee_id, pointing at a valid AGENT in the SAME workspace, and
// deliberately omits assignee_type (changing an assignee's kind, not just
// its identity, is a legitimate request — the field is documented optional
// on every one of these endpoints). The old fallback reused the ROW'S
// CURRENT assignee_type ("user") instead of resolving the new id's actual
// kind, so it looked the agent id up in workspace_members (the user table),
// found nothing, and rejected a valid same-workspace target with "assignee_id
// does not exist in this workspace" — a false reject, not a security hole
// (the wrong-table lookup can only ever under-match, never leak across a
// workspace), but a real usability bug with a misleading message.
//
// The fix (resolveAssigneeType, issue_handler.go) tries "user" then "agent"
// against the SAME workspace when assignee_type is omitted, and persists
// whichever one matches — so assignee_type in the row never goes stale
// relative to a newly-set assignee_id.

func mustBool(t *testing.T, cond bool, msg string) {
	t.Helper()
	if !cond {
		t.Fatal(msg)
	}
}

func TestIssueUpdate_AssigneeTypeOmitted_ResolvesAcrossKindChange(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	// Issue starts assigned to a USER (the workspace owner).
	if _, err := h.db.Exec(`UPDATE missions SET assignee_type = 'user', assignee_id = ? WHERE id = ?`, userID, id); err != nil {
		t.Fatalf("seed initial user assignee: %v", err)
	}

	// PATCH with only assignee_id, pointing at a same-workspace AGENT — no
	// assignee_type in the body. Pre-fix this 400'd.
	body := bytes.NewBufferString(`{"assignee_id":"` + workerID + `"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (same-workspace agent must resolve without assignee_type); body=%s",
			rr.Code, rr.Body.String())
	}

	var gotType, gotID string
	if err := h.db.QueryRow(`SELECT assignee_type, assignee_id FROM missions WHERE id = ?`, id).Scan(&gotType, &gotID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if gotType != "agent" {
		t.Errorf("assignee_type = %q, want %q (must be resolved, not left at the stale 'user')", gotType, "agent")
	}
	if gotID != workerID {
		t.Errorf("assignee_id = %q, want %q", gotID, workerID)
	}
}

func TestIssueUpdate_AssigneeTypeOmitted_NonexistentAssignee_Returns400(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"assignee_id":"does-not-exist-anywhere"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (id matches neither users nor agents in this workspace); body=%s",
			rr.Code, rr.Body.String())
	}
}

func TestBulkUpdate_AssigneeTypeOmitted_ResolvesAcrossKindChange(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	if _, err := h.db.Exec(`UPDATE missions SET assignee_type = 'user', assignee_id = ? WHERE id = ?`, userID, id); err != nil {
		t.Fatalf("seed initial user assignee: %v", err)
	}

	body := bytes.NewBufferString(`{"ids":["` + id + `"],"updates":{"assignee_id":"` + workerID + `"}}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.BulkUpdate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Pre-fix this was silently skipped (updated=0): the wrong-table lookup
	// under the stale "user" type never matched, and bulk's per-item `continue`
	// dropped the item with no way for the caller to tell a real cross-workspace
	// rejection apart from this false reject.
	if resp["updated"] != 1 {
		t.Fatalf("updated = %d, want 1 (same-workspace agent must resolve without assignee_type)", resp["updated"])
	}

	var gotType, gotID string
	if err := h.db.QueryRow(`SELECT assignee_type, assignee_id FROM missions WHERE id = ?`, id).Scan(&gotType, &gotID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if gotType != "agent" {
		t.Errorf("assignee_type = %q, want %q (must be resolved, not left at the stale 'user')", gotType, "agent")
	}
	if gotID != workerID {
		t.Errorf("assignee_id = %q, want %q", gotID, workerID)
	}
}

func TestRecurringUpdate_AssigneeTypeOmitted_ResolvesAcrossKindChange(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *","assignee_type":"user","assignee_id":"` + userID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var created recurringIssueResponse
	mustUnmarshal(t, rr, &created)
	mustBool(t, rr.Code == http.StatusCreated, "create failed: "+rr.Body.String())

	var seededType sql.NullString
	if err := h.db.QueryRow(`SELECT assignee_type FROM recurring_issues WHERE id = ?`, created.ID).Scan(&seededType); err != nil {
		t.Fatalf("check seeded assignee_type: %v", err)
	}
	if seededType.String != "user" {
		t.Fatalf("precondition failed: seeded assignee_type = %q, want %q", seededType.String, "user")
	}

	// PATCH with only assignee_id, pointing at a same-workspace AGENT — no
	// assignee_type in the body. Pre-fix this 400'd.
	body2 := bytes.NewBufferString(`{"assignee_id":"agent-worker"}`)
	req2 := httptest.NewRequest("PATCH", "/", body2)
	req2.SetPathValue("recurringId", created.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (same-workspace agent must resolve without assignee_type); body=%s",
			rr2.Code, rr2.Body.String())
	}

	var gotType, gotID string
	if err := h.db.QueryRow(`SELECT assignee_type, assignee_id FROM recurring_issues WHERE id = ?`, created.ID).Scan(&gotType, &gotID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if gotType != "agent" {
		t.Errorf("assignee_type = %q, want %q (must be resolved, not left at the stale 'user')", gotType, "agent")
	}
	if gotID != "agent-worker" {
		t.Errorf("assignee_id = %q, want %q", gotID, "agent-worker")
	}
}
