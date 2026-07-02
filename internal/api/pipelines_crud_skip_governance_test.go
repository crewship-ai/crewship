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
