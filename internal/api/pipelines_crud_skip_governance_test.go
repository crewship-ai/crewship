package api

// Tests for the OWNER/ADMIN-only skip_governance_gate escape hatch on the
// user-facing routine save path. It is symmetric with skip_test_gate: a
// trusted operator (or the seeder, which runs as OWNER) can force a risky
// definition live as 'active' instead of landing it in the maker-checker
// 'proposed' queue. Lower roles must be refused, and the default (no flag)
// must still classify risky routines as 'proposed'.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// skipGovSaveBody builds a userSaveRequest carrying a RISKY definition (an
// http step). skipTest/skipGov toggle the two independent escape hatches so a
// test can isolate exactly one gate. Note: skip_test_gate itself is
// OWNER/ADMIN-only, so a MANAGER test that wants to reach the governance
// role-check must leave skipTest=false (otherwise it 403s on the test gate
// first).
func skipGovSaveBody(slug, crewID string, skipTest, skipGov bool) string {
	body := map[string]any{
		"slug":                 slug,
		"name":                 slug + " name",
		"description":          "desc",
		"definition":           json.RawMessage(httpRoutineDef()),
		"author_crew_id":       crewID,
		"skip_test_gate":       skipTest,
		"skip_governance_gate": skipGov,
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// OWNER saving a risky routine WITH skip_governance_gate → active, no inbox item.
func TestCovPCSave_SkipGovernanceGate_OwnerRiskyGoesActive_NoInbox(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(skipGovSaveBody("skipgov-active", crewID, true, true)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "skipgov-active"); got != "active" {
		t.Errorf("db status = %q, want active (skip_governance_gate should force live)", got)
	}
	if n := inboxCountForRoutine(t, h, wsID, "skipgov-active"); n != 0 {
		t.Errorf("inbox items = %d, want 0 (no maker-checker review when gate skipped)", n)
	}
}

// MANAGER (below OWNER/ADMIN) attempting skip_governance_gate → 403. skipTest
// is left false so the 403 provably comes from the governance role-check, not
// the (also OWNER/ADMIN-only) test-gate check.
func TestCovPCSave_SkipGovernanceGate_ManagerForbidden_403(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(skipGovSaveBody("skipgov-mgr", crewID, false, true)))
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (skip_governance_gate is OWNER/ADMIN only)", rr.Code)
	}
}

// pendingProposalCount counts the routine's UNRESOLVED maker-checker
// escalations (ResolveBySource flips state to 'resolved' rather than deleting).
func pendingProposalCount(t *testing.T, h *PipelineHandler, wsID, slug string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND source_id = ? AND kind = 'escalation' AND state != 'resolved'`,
		wsID, routineProposalInboxSource(wsID, slug)).Scan(&n); err != nil {
		t.Fatalf("count pending inbox: %v", err)
	}
	return n
}

// A risky routine saved as 'proposed' raises an approval escalation. When an
// OWNER later force-activates it via skip_governance_gate, that escalation must
// be resolved — otherwise a stale "awaiting approval" card lingers for a
// routine that is already live (and a later reject could re-disable it).
func TestCovPCSave_SkipGovernanceGate_ResolvesStaleProposalInbox(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)

	// 1) Risky save without the flag → proposed + a pending escalation.
	req1 := httptest.NewRequest("POST", "/x", strings.NewReader(skipGovSaveBody("stale-inbox", crewID, true, false)))
	req1 = withWorkspaceUser(req1, userID, wsID, "OWNER")
	rr1 := httptest.NewRecorder()
	h.Save(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first save status = %d, want 201; body=%s", rr1.Code, rr1.Body.String())
	}
	if got := routineStatus(t, h, wsID, "stale-inbox"); got != "proposed" {
		t.Fatalf("after first save status = %q, want proposed", got)
	}
	if n := pendingProposalCount(t, h, wsID, "stale-inbox"); n != 1 {
		t.Fatalf("pending escalations after propose = %d, want 1", n)
	}

	// 2) Re-save with skip_governance_gate → active + escalation resolved.
	req2 := httptest.NewRequest("POST", "/x", strings.NewReader(skipGovSaveBody("stale-inbox", crewID, true, true)))
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.Save(rr2, req2)
	if rr2.Code != http.StatusCreated {
		t.Fatalf("re-save status = %d, want 201; body=%s", rr2.Code, rr2.Body.String())
	}
	if got := routineStatus(t, h, wsID, "stale-inbox"); got != "active" {
		t.Errorf("after skip re-save status = %q, want active", got)
	}
	if n := pendingProposalCount(t, h, wsID, "stale-inbox"); n != 0 {
		t.Errorf("pending escalations after skip re-save = %d, want 0 (resolved)", n)
	}
}

// Regression guard: without the flag, an OWNER's risky routine still lands
// 'proposed' — the escape hatch must be opt-in, not the default.
func TestCovPCSave_RiskyWithoutSkipGovernance_StillProposed(t *testing.T) {
	h, userID, wsID, crewID := covPCHandler(t)
	req := httptest.NewRequest("POST", "/x", strings.NewReader(skipGovSaveBody("skipgov-default", crewID, true, false)))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Save(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if got := routineStatus(t, h, wsID, "skipgov-default"); got != "proposed" {
		t.Errorf("db status = %q, want proposed (no skip flag ⇒ maker-checker applies)", got)
	}
}
