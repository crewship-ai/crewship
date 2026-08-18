package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A grant must stay revocable after its subject leaves the workspace.
//
// DeleteGrant used to resolve the subject through the same query that ISSUING
// uses — which JOINs workspace_members for a user, and requires deleted_at IS
// NULL for a crew or an agent. That is the right question when handing out a
// grant and the wrong one when taking it back: the moment the subject left, the
// owner asking to remove the row got a 400 saying no such member, the row
// stayed, and re-adding that person at any role handed their access straight
// back.
//
// Cleaning up after somebody who has gone is precisely when an owner reaches
// for this endpoint.
func TestPageGrants_ADepartedSubjectsGrantCanStillBeRevoked(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('leaver', 'leaver@example.com', 'Leaver')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-leaver', ?, 'leaver', 'MEMBER')`, wsID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	pagesGrant(t, h, wsID, userID, "fleet-201",
		`{"subject_type":"user","subject":"leaver@example.com","level":"write"}`)

	// They leave.
	if _, err := h.db.Exec(`DELETE FROM workspace_members WHERE user_id = 'leaver'`); err != nil {
		t.Fatalf("remove membership: %v", err)
	}

	req := pagesGrantRequest(t, http.MethodDelete,
		"/api/v1/pages/fleet-201/grants?subject_type=user&subject=leaver", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.DeleteGrant(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("revoke after departure: %d %s\nan owner cannot clean up after somebody who has gone",
			rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE subject_id = 'leaver'`).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 0 {
		t.Errorf("%d grant rows survived the revoke — re-adding the user restores their access", n)
	}
}

// Revoking somebody who never held a grant is still a 404, not a silent success.
func TestPageGrants_RevokingAStrangerIsStillNotFound(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	pagesCreate(t, h, wsID, userID, "fleet-201")

	req := pagesGrantRequest(t, http.MethodDelete,
		"/api/v1/pages/fleet-201/grants?subject_type=user&subject=nobody@example.com", wsID, userID, "OWNER", "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	h.DeleteGrant(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no ") {
		t.Errorf("the refusal does not say what was not found: %s", rr.Body.String())
	}
}
