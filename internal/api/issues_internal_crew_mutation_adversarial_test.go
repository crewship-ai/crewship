package api

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Adversarial pass for #1365: the crew guard must hold on EVERY mutation shape
// UpdateStatus accepts (not just a status transition), and it must run BEFORE
// input validation so a wrong-crew caller cannot even probe field-level errors
// on a sibling crew's issue.

func TestSecIssueMutation_UpdateStatus_CrossCrewCommentOnlyRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	// No status/priority — only a comment. The guard must still fire.
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","comment":"cross-crew note","agent_id":"agent-worker"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-crew comment-only update, got %d body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM mission_comments mc JOIN missions m ON m.id = mc.mission_id WHERE m.identifier = ?`,
		ident).Scan(&count); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if count != 0 {
		t.Errorf("cross-crew comment must not be inserted via UpdateStatus, got %d", count)
	}
}

func TestSecIssueMutation_UpdateStatus_CrossCrewPriorityOnlyRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","priority":"high"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-crew priority-only update, got %d body=%s", rr.Code, rr.Body.String())
	}
	var prio sql.NullString
	if err := h.db.QueryRow(`SELECT priority FROM missions WHERE identifier = ?`, ident).Scan(&prio); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	if prio.Valid && prio.String == "high" {
		t.Errorf("cross-crew priority change must not land on crew B's issue")
	}
}

// The crew guard must run BEFORE the agent_id validation. A wrong-crew caller
// with a comment but an EMPTY agent_id must get 403 (security), never 400 — a
// 400 would confirm the issue is reachable enough to reach field validation.
func TestSecIssueMutation_UpdateStatus_CrewGuardBeforeAgentIDValidation(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","comment":"probe"}`) // no agent_id
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("crew guard must fire (403) before agent_id validation (400); got %d body=%s", rr.Code, rr.Body.String())
	}
}
