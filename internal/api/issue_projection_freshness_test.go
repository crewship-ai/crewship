package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Revision counters describe issue-owned state. Joined identity names and
// current capabilities require a fresh GET even when those counters match.
func TestIssueReadTimeProjectionRefreshesNamesAndActions(t *testing.T) {
	h, user, ws, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, ws, crew, lead, "ENG-1", "BACKLOG")
	execOrFatal(t, h.db, `UPDATE missions SET assignee_type='agent',assignee_id=? WHERE id=?`, lead, id)
	get := func() issueResponse {
		t.Helper()
		req := httptest.NewRequest("GET", "/", nil)
		req.SetPathValue("crewId", crew)
		req.SetPathValue("identifier", "ENG-1")
		req = withWorkspaceUser(req, user, ws, "OWNER")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != 200 {
			t.Fatalf("GET %d: %s", rr.Code, rr.Body.String())
		}
		var issue issueResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &issue); err != nil {
			t.Fatal(err)
		}
		return issue
	}
	before := get()
	execOrFatal(t, h.db, `UPDATE agents SET name='Renamed assignee' WHERE id=?`, lead)
	after := get()
	if after.AssigneeName == nil || *after.AssigneeName != "Renamed assignee" {
		t.Fatal("fresh detail retained the old joined name")
	}
	if after.Revision != before.Revision || after.AggregateRevision != before.AggregateRevision {
		t.Fatal("joined identity rename unexpectedly changes issue-owned counters")
	}
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='VIEWER' WHERE workspace_id=? AND user_id=?`, ws, user)
	restricted := get()
	if len(restricted.AllowedActions) != 1 || restricted.AllowedActions[0] != "view" {
		t.Fatalf("fresh detail retained stale actions: %v", restricted.AllowedActions)
	}
	if restricted.Revision != after.Revision || restricted.AggregateRevision != after.AggregateRevision {
		t.Fatal("role change unexpectedly changes issue-owned counters")
	}
}
