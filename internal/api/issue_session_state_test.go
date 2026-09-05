package api

// Tests for issue_session_state.go — the §10.1 session-state transitions
// B4 owns (PRD-ISSUES-AND-ROUTINES-2026 §10.1/§17, work package B4 —
// #2343): pending -> active on a claimed run, active -> idle/error on that
// run ending, and F41's ephemeral-agent reconciliation.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// seedSession inserts one issue_agent_sessions row in the given state,
// against f.target (the fixture's one mentionable agent).
func seedSession(t *testing.T, f *mentionFixture, id, state string) {
	t.Helper()
	seedSessionForAgent(t, f, id, f.target, state)
}

// seedSessionForAgent is seedSession's general form — needed wherever a
// test seeds MORE THAN ONE issue_agent_sessions row against the same
// mission, since UNIQUE(mission_id, agent_id) forbids two rows for the
// same (mission, agent) pair.
func seedSessionForAgent(t *testing.T, f *mentionFixture, id, agentID, state string) {
	t.Helper()
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, datetime('now'), datetime('now'))`,
		id, f.wsID, f.missionID, agentID, state)
}

// seedExtraAgent inserts one more agent in the fixture's crew/workspace —
// needed by tests that seed several issue_agent_sessions rows against the
// same mission, since UNIQUE(mission_id, agent_id) means each needs its
// own agent.
func seedExtraAgent(t *testing.T, f *mentionFixture, id string) {
	t.Helper()
	execOrFatal(t, f.db, `
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		VALUES (?, ?, ?, ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		id, f.wsID, f.crewID, id, id)
}

// seedSessionAssignment inserts one assignment carrying session_id, in the
// given status, against f.target. ensureMissionChat first: chat_id is set
// to f.missionID, and assignments.chat_id has a real FK to chats(id) —
// mentionFixture's own setup never creates that row (real mentions create
// it lazily via ensureMissionChat too), so any test that seeds an
// assignment directly must do the same.
func seedSessionAssignment(t *testing.T, f *mentionFixture, id, sessionID, status string) {
	t.Helper()
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'test task', ?, 1, datetime('now'), ?, ?)`,
		id, f.wsID, f.missionID, f.target, f.target, status, f.missionID, sessionID)
}

func sessionState(t *testing.T, f *mentionFixture, sessionID string) (state string, activeRunID string) {
	t.Helper()
	var activeRun sql.NullString
	if err := f.db.QueryRow(`SELECT state, active_run_id FROM issue_agent_sessions WHERE id = ?`, sessionID).
		Scan(&state, &activeRun); err != nil {
		t.Fatalf("read session %s: %v", sessionID, err)
	}
	return state, activeRun.String
}

// ── activateSessionForAssignment ────────────────────────────────────────

func TestActivateSessionForAssignment_PendingToActive(t *testing.T) {
	f := setupMentionFixture(t)
	seedSession(t, f, "sess_act_1", "pending")
	seedSessionAssignment(t, f, "asg_act_1", "sess_act_1", "RUNNING")

	activateSessionForAssignment(context.Background(), f.db, "asg_act_1")

	state, activeRunID := sessionState(t, f, "sess_act_1")
	if state != "active" {
		t.Errorf("state = %q, want active", state)
	}
	if activeRunID != "asg_act_1" {
		t.Errorf("active_run_id = %q, want asg_act_1", activeRunID)
	}
}

func TestActivateSessionForAssignment_IdleAndErrorAndAwaitingInput_AllReachActive(t *testing.T) {
	// §10.1: idle (delivery arrives), awaiting_input (human answers) and
	// error (human retries) all have an edge into active. A run reaching
	// this function at all already won the run-claim CAS by construction,
	// so every non-closed source state must transition.
	for _, from := range []string{"idle", "awaiting_input", "error", "stale"} {
		t.Run(from, func(t *testing.T) {
			f := setupMentionFixture(t)
			sessID := "sess_act_" + from
			asgID := "asg_act_" + from
			seedSession(t, f, sessID, from)
			seedSessionAssignment(t, f, asgID, sessID, "RUNNING")

			activateSessionForAssignment(context.Background(), f.db, asgID)

			state, activeRunID := sessionState(t, f, sessID)
			if state != "active" {
				t.Errorf("state = %q, want active (from %s)", state, from)
			}
			if activeRunID != asgID {
				t.Errorf("active_run_id = %q, want %s", activeRunID, asgID)
			}
		})
	}
}

func TestActivateSessionForAssignment_ClosedSessionUntouched(t *testing.T) {
	// I10: a closed session refuses new deliveries. Reaching this function
	// for one would mean something upstream already violated that — this
	// function must not paper over it by silently reopening the session.
	f := setupMentionFixture(t)
	seedSession(t, f, "sess_closed", "closed")
	seedSessionAssignment(t, f, "asg_closed", "sess_closed", "RUNNING")

	activateSessionForAssignment(context.Background(), f.db, "asg_closed")

	state, activeRunID := sessionState(t, f, "sess_closed")
	if state != "closed" {
		t.Errorf("state = %q, want closed (untouched)", state)
	}
	if activeRunID != "" {
		t.Errorf("active_run_id = %q, want empty", activeRunID)
	}
}

func TestActivateSessionForAssignment_NoSession_NoOp(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'test task', 'RUNNING', 1, datetime('now'), ?)`,
		"asg_no_sess", f.wsID, f.missionID, f.target, f.target, f.missionID)

	// Must not panic on a NULL session_id.
	activateSessionForAssignment(context.Background(), f.db, "asg_no_sess")
}

// ── settleSessionForAssignment ──────────────────────────────────────────

func TestSettleSessionForAssignment_OutcomeToState(t *testing.T) {
	// §9.6/B6 (#2349): settleSessionForAssignment now resolves the outcome
	// (not the raw status) through orchestrator.RouteForOutcome — this is
	// the test that pins the transition B4 could not reach:
	// NEEDS_HUMAN -> awaiting_input, alongside the two mappings B4 already
	// had right.
	cases := []struct {
		outcome   string
		status    string // the technical status a run with this outcome would carry
		wantState string
	}{
		{orchestrator.OutcomeSucceeded, "COMPLETED", "idle"},
		{orchestrator.OutcomeCancelled, "CANCELLED", "idle"},
		{orchestrator.OutcomeFailed, "FAILED", "error"},
		{orchestrator.OutcomeNeedsHuman, "COMPLETED", "awaiting_input"},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			f := setupMentionFixture(t)
			sessID := "sess_settle_" + tc.outcome
			asgID := "asg_settle_" + tc.outcome
			seedSession(t, f, sessID, "active")
			seedSessionAssignment(t, f, asgID, sessID, tc.status)
			execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET active_run_id = ? WHERE id = ?`, asgID, sessID)

			settleSessionForAssignment(context.Background(), f.db, asgID, tc.outcome)

			state, activeRunID := sessionState(t, f, sessID)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if activeRunID != "" {
				t.Errorf("active_run_id = %q, want cleared", activeRunID)
			}
		})
	}
}

func TestSettleSessionForAssignment_OvertakenByNewerRun_NoOp(t *testing.T) {
	// The exclusivity-slot release race (§9.4/B3): the OLD run's own
	// terminal CAS frees idx_assignments_one_active_per_session's slot in
	// the SAME statement that flips its status, so a brand-new run can
	// claim the session (and activateSessionForAssignment can overwrite
	// active_run_id to the NEW run) before the OLD run's own
	// settleSessionForAssignment call executes. That settle must be a
	// no-op — it must never stomp the newer run's active state.
	f := setupMentionFixture(t)
	seedSession(t, f, "sess_race", "active")
	seedSessionAssignment(t, f, "asg_old", "sess_race", "COMPLETED")
	seedSessionAssignment(t, f, "asg_new", "sess_race", "RUNNING")
	// The new run already won the session (activateSessionForAssignment's
	// own job, simulated directly here).
	execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET active_run_id = 'asg_new', state = 'active' WHERE id = 'sess_race'`)

	// The OLD run's finishAssignment-equivalent settle arrives late.
	settleSessionForAssignment(context.Background(), f.db, "asg_old", orchestrator.OutcomeSucceeded)

	state, activeRunID := sessionState(t, f, "sess_race")
	if state != "active" {
		t.Errorf("state = %q, want active (untouched by the overtaken settle)", state)
	}
	if activeRunID != "asg_new" {
		t.Errorf("active_run_id = %q, want asg_new (the settle for asg_old must not have won)", activeRunID)
	}
}

func TestSettleSessionForAssignment_NoSession_NoOp(t *testing.T) {
	f := setupMentionFixture(t)
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'test task', 'COMPLETED', 1, datetime('now'), ?)`,
		"asg_settle_no_sess", f.wsID, f.missionID, f.target, f.target, f.missionID)
	settleSessionForAssignment(context.Background(), f.db, "asg_settle_no_sess", orchestrator.OutcomeSucceeded)
}

// ── ReconcileStaleActiveSessions, outcome-aware (§9.6, B6, #2349) ───────

func TestReconcileStaleActiveSessions_ResolvesThroughOutcome(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		outcome   sql.NullString // NULL simulates a pre-B6 row with no outcome recorded
		wantState string
	}{
		{"needs_human_outcome_reaches_awaiting_input", "COMPLETED", sql.NullString{String: orchestrator.OutcomeNeedsHuman, Valid: true}, "awaiting_input"},
		{"succeeded_outcome_reaches_idle", "COMPLETED", sql.NullString{String: orchestrator.OutcomeSucceeded, Valid: true}, "idle"},
		{"failed_outcome_reaches_error", "FAILED", sql.NullString{String: orchestrator.OutcomeFailed, Valid: true}, "error"},
		{"null_outcome_falls_back_to_status_failed", "FAILED", sql.NullString{}, "error"},
		{"null_outcome_falls_back_to_status_completed", "COMPLETED", sql.NullString{}, "idle"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMentionFixture(t)
			sessID := "sess_stale_" + tc.name
			asgID := "asg_stale_" + tc.name
			seedSession(t, f, sessID, "active")
			seedSessionAssignment(t, f, asgID, sessID, tc.status)
			execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET active_run_id = ? WHERE id = ?`, asgID, sessID)
			if tc.outcome.Valid {
				execOrFatal(t, f.db, `UPDATE assignments SET outcome = ? WHERE id = ?`, tc.outcome.String, asgID)
			}

			n, err := f.assign.ReconcileStaleActiveSessions(context.Background())
			if err != nil {
				t.Fatalf("ReconcileStaleActiveSessions: %v", err)
			}
			if n != 1 {
				t.Fatalf("reconciled = %d, want 1", n)
			}

			state, activeRunID := sessionState(t, f, sessID)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if activeRunID != "" {
				t.Errorf("active_run_id = %q, want cleared", activeRunID)
			}
		})
	}
}

// TestReconcileStaleActiveSessions_MatchesRouteForOutcome_ForEveryOutcome
// guards against drift between ReconcileStaleActiveSessions's hand-written
// SQL CASE and orchestrator.RouteForOutcome's SessionState — the two encode
// the SAME mapping in two languages (settleSessionForAssignment's Go path,
// and this function's SQL backstop path), and nothing else pins them
// together. Iterating orchestrator.AllOutcomes and computing the WANT value
// from RouteForOutcome itself means a future change to the routing table
// that forgets to update this file's SQL fails here, rather than only
// showing up as a session silently resting in the wrong state after a
// crash.
func TestReconcileStaleActiveSessions_MatchesRouteForOutcome_ForEveryOutcome(t *testing.T) {
	for _, outcome := range orchestrator.AllOutcomes {
		t.Run(outcome, func(t *testing.T) {
			status := "COMPLETED"
			if outcome == orchestrator.OutcomeFailed {
				status = "FAILED"
			} else if outcome == orchestrator.OutcomeCancelled {
				status = "CANCELLED"
			}
			want := orchestrator.RouteForOutcome(outcome).SessionState

			f := setupMentionFixture(t)
			sessID := "sess_route_" + outcome
			asgID := "asg_route_" + outcome
			seedSession(t, f, sessID, "active")
			seedSessionAssignment(t, f, asgID, sessID, status)
			execOrFatal(t, f.db, `UPDATE issue_agent_sessions SET active_run_id = ? WHERE id = ?`, asgID, sessID)
			execOrFatal(t, f.db, `UPDATE assignments SET outcome = ? WHERE id = ?`, outcome, asgID)

			if _, err := f.assign.ReconcileStaleActiveSessions(context.Background()); err != nil {
				t.Fatalf("ReconcileStaleActiveSessions: %v", err)
			}
			state, _ := sessionState(t, f, sessID)
			if state != want {
				t.Errorf("outcome %s: SQL reconcile state = %q, orchestrator.RouteForOutcome says %q — the two have drifted",
					outcome, state, want)
			}
		})
	}
}

// ── ReconcileExpiredEphemeralSessions (F41) ─────────────────────────────

func TestReconcileExpiredEphemeralSessions_ActiveGoesError_OthersGoClosed(t *testing.T) {
	f := setupMentionFixture(t)
	// Six sessions, six DIFFERENT agents (UNIQUE(mission_id, agent_id)
	// forbids more than one session per (mission, agent) pair) — every one
	// ghosted (F41's trigger — the same expired_at flip
	// ephemeral.SweepExpiredAgents performs), so the reconcile's own
	// `agent_id IN (...)` predicate matches all six regardless of state.
	agents := []string{"agent_eph_active", "agent_eph_pending", "agent_eph_idle", "agent_eph_awaiting", "agent_eph_closed", "agent_eph_stale"}
	for _, id := range agents {
		seedExtraAgent(t, f, id)
		execOrFatal(t, f.db, `UPDATE agents SET expired_at = datetime('now') WHERE id = ?`, id)
	}

	seedSessionForAgent(t, f, "sess_eph_active", "agent_eph_active", "active")
	seedSessionForAgent(t, f, "sess_eph_pending", "agent_eph_pending", "pending")
	seedSessionForAgent(t, f, "sess_eph_idle", "agent_eph_idle", "idle")
	seedSessionForAgent(t, f, "sess_eph_awaiting", "agent_eph_awaiting", "awaiting_input")
	seedSessionForAgent(t, f, "sess_eph_closed", "agent_eph_closed", "closed")
	seedSessionForAgent(t, f, "sess_eph_stale", "agent_eph_stale", "stale")

	n, err := f.assign.ReconcileExpiredEphemeralSessions(context.Background())
	if err != nil {
		t.Fatalf("ReconcileExpiredEphemeralSessions: %v", err)
	}
	if n != 4 {
		t.Errorf("reconciled = %d, want 4 (active, pending, idle, awaiting_input)", n)
	}

	want := map[string]string{
		"sess_eph_active":   "error",
		"sess_eph_pending":  "closed",
		"sess_eph_idle":     "closed",
		"sess_eph_awaiting": "closed",
		"sess_eph_closed":   "closed", // untouched, was already closed
		"sess_eph_stale":    "stale",  // untouched, sweep does not touch stale
	}
	for id, wantState := range want {
		state, _ := sessionState(t, f, id)
		if state != wantState {
			t.Errorf("%s state = %q, want %q", id, state, wantState)
		}
	}
}

func TestReconcileExpiredEphemeralSessions_NonExpiredAgent_Untouched(t *testing.T) {
	f := setupMentionFixture(t)
	seedSession(t, f, "sess_alive_agent", "active")

	n, err := f.assign.ReconcileExpiredEphemeralSessions(context.Background())
	if err != nil {
		t.Fatalf("ReconcileExpiredEphemeralSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("reconciled = %d, want 0 (agent not expired)", n)
	}
	state, _ := sessionState(t, f, "sess_alive_agent")
	if state != "active" {
		t.Errorf("state = %q, want active (untouched)", state)
	}
}

// ── End-to-end observable behaviour: owner died -> recovered, visible via
//    `issue runs` / `issue sessions` (the accept line's own words) ───────

// TestOwnerDied_RecoveredByLeaseSweep_VisibleInRunsAndSessions is the
// user-observable proof the accept line asks for: "a killed process's runs
// recover after lease expiry" — not just a DB-row assertion, but the same
// two read surfaces a human or the CLI actually looks at
// (`crewship issue runs` / `crewship issue sessions`, backed by ListRuns /
// ListSessions) showing the recovered state.
func TestOwnerDied_RecoveredByLeaseSweep_VisibleInRunsAndSessions(t *testing.T) {
	f := setupMentionFixture(t)
	sessionID := "sess_owner_died"
	assignmentID := "asg_owner_died"
	seedSession(t, f, sessionID, "pending")
	seedSessionAssignment(t, f, assignmentID, sessionID, "RUNNING")
	activateSessionForAssignment(context.Background(), f.db, assignmentID)

	// The owning process "died": its lease expired long ago and nothing
	// has renewed it since.
	longExpired := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	execOrFatal(t, f.db, `UPDATE assignments SET lease_owner = ?, lease_expires_at = ? WHERE id = ?`,
		"dead-process:999", longExpired, assignmentID)

	n, err := f.assign.SweepExpiredLeases(context.Background())
	if err != nil {
		t.Fatalf("SweepExpiredLeases: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped = %d, want 1", n)
	}

	// Observable surface 1: `issue runs`.
	rr := httptest.NewRecorder()
	f.issues.ListRuns(rr, issueRunsRequest(t, f.userID, f.wsID, f.crewID, f.ident))
	if rr.Code != 200 {
		t.Fatalf("ListRuns status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var runs []issueRunDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v; body=%s", err, rr.Body.String())
	}
	found := false
	for _, run := range runs {
		if run.ID != assignmentID {
			continue
		}
		found = true
		if run.Status != "FAILED" {
			t.Errorf("run status = %q, want FAILED", run.Status)
		}
		if run.ErrorMessage == "" || !strings.Contains(run.ErrorMessage, "lease expired") {
			t.Errorf("run error_message = %q, want it to name the lease expiry", run.ErrorMessage)
		}
	}
	if !found {
		t.Fatalf("recovered run %s not present in ListRuns output: %+v", assignmentID, runs)
	}

	// Observable surface 2: `issue sessions`.
	rr2 := httptest.NewRecorder()
	req2 := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/crews/"+f.crewID+"/issues/"+f.ident+"/sessions", nil),
		f.userID, f.wsID, "OWNER",
	)
	req2.SetPathValue("crewId", f.crewID)
	req2.SetPathValue("identifier", f.ident)
	f.issues.ListSessions(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("ListSessions status = %d, body=%s", rr2.Code, rr2.Body.String())
	}
	var sessions []issueAgentSessionDTO
	if err := json.Unmarshal(rr2.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions: %v; body=%s", err, rr2.Body.String())
	}
	sessFound := false
	for _, s := range sessions {
		if s.ID != sessionID {
			continue
		}
		sessFound = true
		if s.State != "error" {
			t.Errorf("session state = %q, want error", s.State)
		}
		if s.ActiveRunID != "" {
			t.Errorf("session active_run_id = %q, want empty", s.ActiveRunID)
		}
	}
	if !sessFound {
		t.Fatalf("session %s not present in ListSessions output: %+v", sessionID, sessions)
	}
}
