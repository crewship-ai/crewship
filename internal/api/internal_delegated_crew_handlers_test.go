package api

// Proof that the delegated-authorship gate is WIRED, not merely present.
// internal_delegated_crew_test.go pins the rules at the helper; these two
// drive the real handlers, because a correct helper nobody calls is exactly
// the shape this bug had in the first place.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// guideDelegationFixture stands up a workspace holding the Guide's own crew
// and one crew a person created through the wizard — the exact pair
// onboarding produces.
func guideDelegationFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	guideCrew := seedCrewRowKind(t, db, "c-guide", wsID, "Crewship Guide", "_crewship-setup", setupCrewKindSetup)
	// full autonomy, as onboarding_setup_crew.go gives the real one — so a
	// refusal in these tests can only be the delegation gate, never the
	// policy gate.
	if _, err := db.Exec(`UPDATE crews SET autonomy_level = 'full' WHERE id = ?`, guideCrew); err != nil {
		t.Fatalf("raise guide autonomy: %v", err)
	}
	ownerCrew := seedCrewRowKind(t, db, "c-watch", wsID, "Uptime Watch", "uptime-watch", setupCrewKindStandard)
	seedAgentRow(t, db, "a-watch", wsID, ownerCrew, "Watcher", "watcher", "LEAD")

	h := NewPageHandler(db, nil, newTestLogger())
	h.SetPolicyResolver(policy.NewResolver(db))
	return h, wsID, guideCrew, ownerCrew
}

func pageSaveBody(wsID, crewID, targetSlug, ownerSlug, producerSlug string) string {
	b := map[string]any{
		"workspace_id": wsID,
		"crew_id":      crewID,
		"name":         "Dostupnost",
		"panels": []map[string]any{{
			"id":          "p1",
			"schema":      "status.v1",
			"owner":       "crew/" + ownerSlug,
			"producer":    "agent/" + producerSlug,
			"sla_seconds": 30,
			"span":        4,
		}},
	}
	if targetSlug != "" {
		b["target_crew_slug"] = targetSlug
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

// The defect, at the handler. Before the gate the Guide simply owned the
// page: author crew _crewship-setup, panels pointing at the Guide's own
// agent, and the person's actual crew nowhere in it.
func TestPageInternalSave_SetupCrewCannotOwnAPage(t *testing.T) {
	h, wsID, guideCrew, _ := guideDelegationFixture(t)
	body := pageSaveBody(wsID, guideCrew, "", "_crewship-setup", "watcher")
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req = req.WithContext(crewBoundCtx1222(wsID, guideCrew))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "target_crew_slug") {
		t.Errorf("refusal does not tell the agent how to retry: %s", rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if n != 0 {
		t.Errorf("pages created = %d, want 0", n)
	}
}

// The delegated path end to end: the Guide authors, the person's crew owns.
func TestPageInternalSave_DelegatedPageIsOwnedByTheNamedCrew(t *testing.T) {
	h, wsID, guideCrew, ownerCrew := guideDelegationFixture(t)
	body := pageSaveBody(wsID, guideCrew, "uptime-watch", "uptime-watch", "watcher")
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req = req.WithContext(crewBoundCtx1222(wsID, guideCrew))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var crewID string
	if err := h.db.QueryRow(
		`SELECT COALESCE(owner_crew_id, '') FROM pages WHERE workspace_id = ? AND slug = 'dostupnost'`,
		wsID).Scan(&crewID); err != nil {
		t.Fatalf("read page crew: %v", err)
	}
	if crewID != ownerCrew {
		t.Errorf("page crew_id = %q, want %q (the crew it was built for)", crewID, ownerCrew)
	}
}

// The autonomy gate asks about the ACTOR. A brand-new crew is `guided` by
// default, and holding the Guide's page because the crew it is for has not
// been granted page-creation rights would make onboarding unable to finish
// its own job — while telling the person nothing they could act on.
func TestPageInternalSave_DelegationIsGatedOnTheGuideNotTheTarget(t *testing.T) {
	h, wsID, guideCrew, ownerCrew := guideDelegationFixture(t)
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level = 'strict' WHERE id = ?`, ownerCrew); err != nil {
		t.Fatalf("lower target autonomy: %v", err)
	}
	body := pageSaveBody(wsID, guideCrew, "uptime-watch", "uptime-watch", "watcher")
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req = req.WithContext(crewBoundCtx1222(wsID, guideCrew))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// And the exception stays shut for everyone else: an ordinary crew naming
// another crew is the cross-crew escalation the sidecar's trust model
// promises is impossible.
func TestPageInternalSave_OrdinaryCrewCannotDelegate(t *testing.T) {
	h, wsID, _, ownerCrew := guideDelegationFixture(t)
	other := seedCrewRowKind(t, h.db, "c-other", wsID, "Other", "other-crew", setupCrewKindStandard)
	if _, err := h.db.Exec(`UPDATE crews SET autonomy_level = 'full' WHERE id = ?`, other); err != nil {
		t.Fatalf("raise other autonomy: %v", err)
	}
	body := pageSaveBody(wsID, other, "uptime-watch", "uptime-watch", "watcher")
	req := httptest.NewRequest("POST", "/api/v1/internal/pages/save", bytes.NewBufferString(body))
	req = req.WithContext(crewBoundCtx1222(wsID, other))
	rr := httptest.NewRecorder()
	h.InternalSave(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE owner_crew_id = ?`, ownerCrew).Scan(&n); err != nil {
		t.Fatalf("count pages: %v", err)
	}
	if n != 0 {
		t.Errorf("pages landed on the target crew = %d, want 0", n)
	}
}
