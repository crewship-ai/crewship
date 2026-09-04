package api

// issue_session_state.go — the §10.1 session state-machine transitions B4
// owns (PRD-ISSUES-AND-ROUTINES-2026 §10.1/§17, work package B4 — #2343):
// `pending -> active` on a claimed run, `active -> idle`/`error` on that
// run ending, the lease sweeper's `active -> error`, and F41's ephemeral-
// agent reconciliation. issue_sessions.go (B1) creates a session in
// 'pending' and stops there; everything past that is this file.
//
// Two entry points, both keyed off assignments.session_id (nullable — a
// mission task, a root /assign, or any run dispatched with the
// issue_agent_sessions flag off has none, and both functions below are
// no-ops for such a row):
//
//   - activateSessionForAssignment, called from runAssignment right after
//     the "Mark assignment as RUNNING" stamp (assignments_run.go) — the
//     same unconditional point stampInitialLease hooks (assignments_lease.go).
//     "only a live run may hold active" (§10.1) — this is where a run
//     becomes live.
//   - settleSessionForAssignment, called from finishAssignment right after
//     the terminal-state CAS is WON (assignments_run.go) — status is
//     already COMPLETED/FAILED/CANCELLED by the time this runs, so the
//     session transition and the run's own terminal status can never
//     disagree about whether the run is still live.
//
// What is deliberately NOT implemented here: `active -> awaiting_input`.
// That transition fires on outcome=NEEDS_HUMAN (§9.6), and `outcome` does
// not exist on `assignments` until B6 — without it there is no way to
// distinguish "finished, nothing more needed" from "finished, blocked on a
// human" other than the run's own status, which this file uses for the two
// transitions status CAN answer honestly: COMPLETED/CANCELLED -> idle
// (nothing further is owed), FAILED -> error (something needs a retry).
// Naming this rather than guessing at NEEDS_HUMAN from status keeps the
// state machine honest until B6 gives it the real signal.

import (
	"context"
	"database/sql"
	"time"
)

// activateSessionForAssignment moves assignmentID's session to 'active' and
// stamps active_run_id, if the assignment has a session at all.
//
// `state != 'closed'` rather than an explicit list of source states: every
// non-closed state (pending, idle, stale, awaiting_input, error) has an
// incoming edge to active somewhere in §10.1's diagram (a claimed run, a
// delivery arriving, a human answering, a human retrying), and a session
// that reaches this function at all just won a live run's claim by
// construction — idx_assignments_one_active_per_session (B3) guarantees
// nothing else can be RUNNING for the same session concurrently. 'closed'
// is excluded because I10 says a closed session "refuses new deliveries" —
// reaching this function for a closed session would mean something upstream
// already violated that, and clobbering the closed state here would hide
// the bug rather than surface it.
//
// Best-effort: a read or write failure here degrades to "the session's
// displayed state lags the run" — a UI/observability gap, not a
// correctness one (the run itself, and the exclusivity index, do not
// depend on this write landing). Matches every other post-dispatch side
// effect in this package (hooks, hub broadcasts).
func activateSessionForAssignment(ctx context.Context, db *sql.DB, assignmentID string) {
	var sessionID sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT session_id FROM assignments WHERE id = ?`, assignmentID,
	).Scan(&sessionID); err != nil || !sessionID.Valid || sessionID.String == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.ExecContext(ctx, `
		UPDATE issue_agent_sessions
		   SET state = 'active', active_run_id = ?, last_activity_at = ?, updated_at = ?
		 WHERE id = ? AND state != 'closed'`,
		assignmentID, now, now, sessionID.String)
}

// settleSessionForAssignment moves assignmentID's session off 'active' once
// the run has reached a terminal status — COMPLETED/CANCELLED -> idle
// (nothing further owed), FAILED -> error (needs a retry). See the file
// header for why NEEDS_HUMAN/awaiting_input is not reachable here.
//
// `AND active_run_id = ?` is the guard that makes this safe against the
// race the exclusivity slot's own release creates: finishAssignment's
// terminal CAS frees idx_assignments_one_active_per_session's slot in the
// SAME statement that flips status, which means a brand-new mention can
// resolve-and-insert (and even claim RUNNING) a NEW run for this session —
// and this function's own caller run — BEFORE this call executes. Without
// the guard, a slow settle for the OLD run would stomp the NEW run's
// active_run_id/active state right out from under it. With it, a settle
// that has been overtaken is simply a no-op: whichever run's id
// active_run_id currently holds is the one whose eventual settle gets to
// write the next transition.
func settleSessionForAssignment(ctx context.Context, db *sql.DB, assignmentID, runStatus string) {
	var sessionID sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT session_id FROM assignments WHERE id = ?`, assignmentID,
	).Scan(&sessionID); err != nil || !sessionID.Valid || sessionID.String == "" {
		return
	}
	newState := "idle"
	if runStatus == "FAILED" {
		newState = "error"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.ExecContext(ctx, `
		UPDATE issue_agent_sessions
		   SET state = ?, active_run_id = NULL, last_activity_at = ?, updated_at = ?
		 WHERE id = ? AND active_run_id = ?`,
		newState, now, now, sessionID.String, assignmentID)
}

// ReconcileExpiredEphemeralSessions closes or errors every issue_agent_
// sessions row whose agent has since been ghosted (agents.expired_at set)
// — F41: "an ephemeral agent expiring mid-session closes or errors that
// session, not leave it rendered active". Two independent expiry clocks
// (ephemeral.SweepExpiredAgents flips agents.expired_at on its own 5-minute
// ticker; this table's own lease clock governs assignments) would
// otherwise disagree silently: a session can sit 'active' over an agent
// the ephemeral sweeper has already ghosted until an unrelated read
// happens to notice.
//
// 'active' -> 'error' (a live run was orphaned by its own agent vanishing —
// the same "something needs a retry" signal a lease-expiry reap gives);
// every other non-terminal state ('pending', 'idle', 'awaiting_input') ->
// 'closed' (no live run to orphan, and a ghosted agent will never resume
// one — I10's "closed refuses new deliveries" is the correct resting
// state, not idle). 'closed' and 'stale' rows are already excluded by the
// WHERE and left untouched.
//
// One bulk UPDATE, not a per-row loop with a journal emit: unlike the
// lease sweeper (which reaps assignments — rows with their own completion
// signal contract via finishAssignment), a session row has no consumer
// that reads a journal entry to learn it changed; ListSessions reads the
// table directly. Idempotent by construction — once a row leaves the
// WHERE's state set it can never match again — so a crashed reconcile that
// updated some rows and not others is corrected by the next tick, exactly
// like ephemeral.SweepExpiredAgents' own per-tick idempotence.
func (h *AssignmentHandler) ReconcileExpiredEphemeralSessions(ctx context.Context) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(ctx, `
		UPDATE issue_agent_sessions
		   SET state = CASE WHEN state = 'active' THEN 'error' ELSE 'closed' END,
		       active_run_id = NULL,
		       updated_at = ?
		 WHERE state IN ('pending','active','awaiting_input','idle')
		   AND agent_id IN (SELECT id FROM agents WHERE expired_at IS NOT NULL)`,
		now)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
