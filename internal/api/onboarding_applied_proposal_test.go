package api

// Tests for onboarding.go's applied_proposal_id branch — the fix for the
// "Launch deploys a SECOND crew" bug: once the setup agent's proposal card
// has been applied (POST /onboarding/proposals/{id}/apply), clicking Launch
// used to call POST /onboarding/setup unconditionally, which ran the blank/
// single-agent deploy path and created a second crew+agent alongside the one
// the proposal already made real. These tests pin that Setup now (a) trusts
// an applied_proposal_id only after verifying it, and (b) when it's valid,
// persists prefs/telemetry/completion WITHOUT deploying anything.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/services"
)

// ---- valid applied proposal: no second crew, prefs + telemetry persisted,
//      onboarding completed ----

func TestOnboardingSetup_AppliedProposalID_Valid_SkipsSecondDeployPersistsPrefs(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)

	oph, userID, wsID := opFixture(t)
	opSeedTemplate(t, oph.db, wsID, "eng-crew", opTwoAgentRoster())
	_, proposal := opCreate(t, oph, userID, wsID, map[string]string{
		"crew_name": "Chat Crew", "template_slug": "eng-crew",
	})
	applyRR := opApply(oph, userID, wsID, proposal.ID, "OWNER", nil)
	if applyRR.Code != http.StatusCreated {
		t.Fatalf("apply status = %d, want 201, body=%s", applyRR.Code, applyRR.Body.String())
	}
	var applied onboardingProposalApplyResponse
	if err := json.Unmarshal(applyRR.Body.Bytes(), &applied); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applied.Crew.CrewID == "" {
		t.Fatal("apply produced no crew id — fixture broken, can't test the skip-deploy path")
	}

	h := NewOnboardingHandler(oph.db, nil, testLogger())
	body := fmt.Sprintf(`{
		"applied_proposal_id": %q,
		"workspace_name": "Renamed WS",
		"preferred_language": "Czech",
		"telemetry_opt_in": true
	}`, proposal.ID)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), applied.Crew.CrewID) {
		t.Errorf("body = %s, want it to name the applied crew %q so the frontend can route there", w.Body.String(), applied.Crew.CrewID)
	}

	// The symptom this prevents: a second crew created by Setup alongside
	// the one Apply already made real.
	var crewCount int
	if err := oph.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL`, wsID).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count = %d, want 1 (Setup must not deploy a second crew)", crewCount)
	}

	// Prefs + telemetry + completion — persisted exactly as the other two
	// branches persist them. Silently dropping the consent choice here is
	// the failure mode the naive fix (calling /onboarding/complete instead)
	// would have produced.
	var lang string
	if err := oph.db.QueryRow(`SELECT preferred_language FROM workspaces WHERE id = ?`, wsID).Scan(&lang); err != nil {
		t.Fatalf("read preferred_language: %v", err)
	}
	if lang != "Czech" {
		t.Errorf("preferred_language = %q, want Czech", lang)
	}
	var wsName string
	if err := oph.db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, wsID).Scan(&wsName); err != nil {
		t.Fatalf("read workspace name: %v", err)
	}
	if wsName != "Renamed WS" {
		t.Errorf("workspace name = %q, want Renamed WS", wsName)
	}
	var telemetryVal string
	if err := oph.db.QueryRow(`SELECT value FROM app_settings WHERE key = 'telemetry_opt_in'`).Scan(&telemetryVal); err != nil {
		t.Fatalf("read telemetry_opt_in: %v", err)
	}
	if telemetryVal != "1" {
		t.Errorf("telemetry_opt_in = %q, want 1", telemetryVal)
	}
	var completed bool
	if err := oph.db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&completed); err != nil {
		t.Fatalf("readback onboarding_completed: %v", err)
	}
	if !completed {
		t.Error("onboarding_completed not set after applied-proposal setup")
	}
}

// ---- refused cases: unknown id, cross-workspace id, not-yet-applied id.
//      Each must be refused with a 400 and must NOT create a crew or
//      complete onboarding — silently ignoring a bad id would fall through
//      to the old behaviour (a second deploy) or worse, complete onboarding
//      with no crew at all. ----

func TestOnboardingSetup_AppliedProposalID_Refused(t *testing.T) {
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)

	t.Run("unknown_id", func(t *testing.T) {
		// Symptom prevented: a typo'd or stale id must not be trusted
		// blindly — that would skip crew creation entirely and complete
		// onboarding with no crew at all.
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		h := NewOnboardingHandler(db, nil, testLogger())

		body := `{"applied_proposal_id":"does-not-exist"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		w := httptest.NewRecorder()
		h.Setup(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (unknown proposal id), body=%s", w.Code, w.Body.String())
		}
		requireNoCrewsAndIncomplete(t, db, wsID, userID)
	})

	t.Run("cross_workspace", func(t *testing.T) {
		// Symptom prevented: a proposal id from a workspace the caller
		// doesn't belong to must not let them skip a deploy in THEIR
		// workspace (or worse, get routed to someone else's crew).
		db := setupTestDB(t)
		userA := seedTestUser(t, db)
		wsA := seedTestWorkspace(t, db, userA)

		userB := "user-b"
		execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'b@example.com', 'User B')`, userB)
		wsB := "workspace-b"
		execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'WS B', 'ws-b')`, wsB)
		execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('mb', ?, ?, 'OWNER')`, wsB, userB)

		oph := NewOnboardingProposalHandler(db, newTestLogger())
		opSeedTemplate(t, db, wsB, "eng-crew", opTwoAgentRoster())
		_, proposal := opCreate(t, oph, userB, wsB, map[string]string{
			"crew_name": "B Crew", "template_slug": "eng-crew",
		})
		if rr := opApply(oph, userB, wsB, proposal.ID, "OWNER", nil); rr.Code != http.StatusCreated {
			t.Fatalf("apply for workspace B failed: %d, body=%s", rr.Code, rr.Body.String())
		}

		h := NewOnboardingHandler(db, nil, testLogger())
		body := fmt.Sprintf(`{"applied_proposal_id":%q}`, proposal.ID)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userA}))
		w := httptest.NewRecorder()
		h.Setup(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (cross-workspace proposal id), body=%s", w.Code, w.Body.String())
		}
		requireNoCrewsAndIncomplete(t, db, wsA, userA)
	})

	t.Run("not_yet_applied", func(t *testing.T) {
		// Symptom prevented: a PENDING proposal (Create ran, Apply never
		// clicked) must not be treated as "a crew already exists" — that
		// would complete onboarding with no crew ever created.
		oph, userID, wsID := opFixture(t)
		opSeedTemplate(t, oph.db, wsID, "eng-crew", opTwoAgentRoster())
		_, proposal := opCreate(t, oph, userID, wsID, map[string]string{
			"crew_name": "Pending Crew", "template_slug": "eng-crew",
		})
		// Deliberately not applied.

		h := NewOnboardingHandler(oph.db, nil, testLogger())
		body := fmt.Sprintf(`{"applied_proposal_id":%q}`, proposal.ID)
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		w := httptest.NewRecorder()
		h.Setup(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (not yet applied), body=%s", w.Code, w.Body.String())
		}
		requireNoCrewsAndIncomplete(t, oph.db, wsID, userID)
	})
}

// requireNoCrewsAndIncomplete asserts a refused applied_proposal_id left no
// trace: no crew in the workspace, onboarding not marked complete.
func requireNoCrewsAndIncomplete(t *testing.T, db *sql.DB, wsID, userID string) {
	t.Helper()
	var crewCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 0 {
		t.Errorf("crew count = %d, want 0 (refused applied_proposal_id must not deploy)", crewCount)
	}
	var completed bool
	if err := db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&completed); err != nil {
		t.Fatalf("readback onboarding_completed: %v", err)
	}
	if completed {
		t.Error("onboarding_completed must stay false when applied_proposal_id is refused")
	}
}

// ---- without the field: today's behaviour is unchanged ----

func TestOnboardingSetup_AppliedProposalID_Omitted_BlankPathUnchanged(t *testing.T) {
	// Symptom prevented: adding the applied_proposal_id branch check must
	// not change behaviour for a submission that never sends it — the
	// blank/single-agent deploy path (still the only path for non-chat
	// onboarding) has to keep creating exactly one crew, same as before
	// this change.
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	svc := services.NewOnboardingService(db, testLogger(), generateCUID)
	h := NewOnboardingHandler(db, svc, testLogger())

	body := `{"crew_name":"Blank Crew","agent_name":"Agent One"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	h.Setup(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (blank path unaffected), body=%s", w.Code, w.Body.String())
	}
	var crewCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count = %d, want 1", crewCount)
	}
	var completed bool
	if err := db.QueryRow(`SELECT onboarding_completed FROM users WHERE id = ?`, userID).Scan(&completed); err != nil {
		t.Fatalf("readback onboarding_completed: %v", err)
	}
	if !completed {
		t.Error("onboarding_completed should be true after the blank path succeeds")
	}
}
