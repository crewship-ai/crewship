package api

// TestMentions_FollowUpDuringActiveSessionIsQueuedNotClaimedByTheRunInFlight
// is the mentionRecorder half of B3's accept line (PRD-ISSUES-AND-ROUTINES-
// 2026 §9.4/§17, #2339):
//
//	"two comments 2s apart produce one run and two consumed deliveries"
//
// The DB-level half (the exclusivity index itself, and
// resolveSessionAndInsertAssignment's no-TOCTOU insert path) is proven
// directly against delegation_limits.go in delegation_limits_session_test.go.
// The "eventually consumed" half — a *sessionBusyError's delivery folded
// into one new run once the run it collided with actually finishes, with
// the follow-up text reaching that new run's own brief — is proven against
// the real dispatch path in issue_session_followups_test.go.
//
// THIS file proves the piece between those two: that mentionRecorder — the
// caller of DispatchMention — reads a *sessionBusyError and releases the
// delivery back to 'pending' (releaseClaimedDelivery, issue_deliveries.go)
// rather than claiming it under the run already in flight. An earlier
// revision claimed it there instead, and review on #2342 caught the
// consequence: consumeDeliveriesForRun would mark it 'consumed' the moment
// that run finished, whether or not the run had ever seen the second
// comment — a run captures its own task as a Go value at dispatch time
// (assignments_run.go) and never re-reads the row, so it could not have.
//
// Uses a hand-written stub dispatcher rather than the real AssignmentHandler
// because the real one's DispatchMention races a background goroutine
// (runAssignment) that this repo's other mention tests rely on finishing
// fast (no resolver wired in any test fixture) — too fast to reliably
// observe an in-flight second mention without a flaky sleep. The stub
// instead deterministically reproduces the exact contract DispatchMention
// promises under B3: first call succeeds with a fresh id; every later call
// for the SAME target returns that same id wrapped in a *sessionBusyError,
// exactly as resolveSessionAndInsertAssignment does once the index is
// engaged.

import (
	"context"
	"sync"
	"testing"
)

// sessionBusyStubDispatcher reproduces DispatchMention's B3 contract without
// a real container/orchestrator: the first call for a given target succeeds
// with a fresh assignment id; every later call for the SAME target returns
// that SAME id wrapped in a *sessionBusyError, exactly as
// resolveSessionAndInsertAssignment does once idx_assignments_one_active_per_session
// is holding that session's slot.
type sessionBusyStubDispatcher struct {
	mu    sync.Mutex
	total int
	seen  map[string]string // target agent id -> the id its first call returned
}

func (s *sessionBusyStubDispatcher) DispatchMention(_ context.Context, req mentionDispatchRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total++
	if s.seen == nil {
		s.seen = map[string]string{}
	}
	if id, ok := s.seen[req.TargetAgentID]; ok {
		return id, &sessionBusyError{SessionID: "sess_stub_" + req.TargetAgentID, ActiveAssignmentID: id}
	}
	id := "asg_stub_" + req.TargetAgentID
	s.seen[req.TargetAgentID] = id
	return id, nil
}

func TestMentions_FollowUpDuringActiveSessionIsQueuedNotClaimedByTheRunInFlight(t *testing.T) {
	f := setupMentionFixture(t)

	stub := &sessionBusyStubDispatcher{}
	f.issues.SetMentionDispatcher(stub)
	f.internal.SetMentionDispatcher(stub)

	activeRunID := "asg_stub_" + f.target
	// assignments.chat_id has an FK to chats — the real DispatchMention
	// calls ensureMissionChat before its own insert; do the same here so
	// this test's manual seed row satisfies it.
	if err := ensureMissionChat(context.Background(), f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	// The stub never inserts into `assignments` itself (it is not the real
	// AssignmentHandler) — seed the row its id names so the FK on
	// mission_comment_mentions.assignment_id is satisfied, matching what a
	// real DispatchMention call would have written.
	execOrFatal(t, f.db, `
		INSERT INTO assignments
		    (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'first mention', 'RUNNING', 1, datetime('now'), ?)`,
		activeRunID, f.wsID, f.missionID, f.target, f.target, f.missionID)

	// Comment 1: the mentioned agent's session has no live run yet — a
	// fresh dispatch.
	f.comment(t, "first "+mentionToken("lead", f.target))
	// Comment 2, "2s later": the session's slot is already held by
	// activeRunID (per the stub's contract) — must be queued, not folded
	// into a second dispatch, and NOT claimed under a run that cannot see
	// it.
	f.comment(t, "second "+mentionToken("lead", f.target))

	if got := stub.calls(); got != 2 {
		t.Fatalf("dispatcher was called %d times, want 2 (one per comment)", got)
	}

	// "one run": only the ONE assignment row this test itself seeded exists
	// — the stub never wrote a second one, and neither did the dispatch
	// path (which is exactly the property under test: a session-busy
	// result must not cause a second insert anywhere in this path).
	var runCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE mission_id = ?`, f.missionID).Scan(&runCount); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if runCount != 1 {
		t.Fatalf("assignments for this mission = %d, want 1 (\"one run\")", runCount)
	}

	rows, err := f.db.Query(`
		SELECT id, dispatch_state, state, COALESCE(claimed_by_run_id,'')
		  FROM mission_comment_mentions WHERE mission_id = ? ORDER BY position, created_at`, f.missionID)
	if err != nil {
		t.Fatalf("query deliveries: %v", err)
	}
	defer rows.Close()
	type row struct{ id, dispatchState, state, claimedBy string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.dispatchState, &r.state, &r.claimedBy); err != nil {
			t.Fatalf("scan delivery: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("mission_comment_mentions rows = %d, want 2 (one per comment)", len(got))
	}

	// The first delivery is the fresh dispatch: claimed by the run that IS
	// processing it, dispatch_state 'dispatched'.
	if got[0].dispatchState != mentionDispatchDispatched {
		t.Errorf("first delivery dispatch_state = %q, want %q", got[0].dispatchState, mentionDispatchDispatched)
	}
	if got[0].state != "claimed" {
		t.Errorf("first delivery state = %q, want claimed (the run it belongs to has not finished)", got[0].state)
	}
	if got[0].claimedBy != activeRunID {
		t.Errorf("first delivery claimed_by_run_id = %q, want %q", got[0].claimedBy, activeRunID)
	}

	// The second is B3's queued outcome — dispatch_state distinguishes it
	// from the first (mentionDispatchQueued, not mentionDispatchDispatched,
	// so an operator can tell "folded in" apart from "started a new run"),
	// but critically it is back at 'pending', NOT 'claimed' under
	// activeRunID: that run cannot see this comment (see the file doc
	// comment for why), so nothing may mark it consumed on that run's
	// account. dispatchQueuedFollowUpsForSession (assignments_run.go) is
	// what actually claims and consumes it, once a run that CAN see the
	// text is dispatched — proven end to end in
	// issue_session_followups_test.go, not here.
	if got[1].dispatchState != mentionDispatchQueued {
		t.Errorf("second delivery dispatch_state = %q, want %q", got[1].dispatchState, mentionDispatchQueued)
	}
	if got[1].state != "pending" {
		t.Errorf("second delivery state = %q, want pending — it must not be claimed by a run that cannot see it", got[1].state)
	}
	if got[1].claimedBy != "" {
		t.Errorf("second delivery claimed_by_run_id = %q, want empty", got[1].claimedBy)
	}

	// The winner's own delivery still consumes normally once it finishes —
	// this half of the pipeline is unchanged by B3's redesign.
	n, err := consumeDeliveriesForRun(context.Background(), f.db, activeRunID)
	if err != nil {
		t.Fatalf("consumeDeliveriesForRun: %v", err)
	}
	if n != 1 {
		t.Fatalf("consumeDeliveriesForRun consumed %d rows, want 1 (the first delivery only — the second was never claimed by this run)", n)
	}
}

func (s *sessionBusyStubDispatcher) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}
