package api

// TestMentions_FollowUpDuringActiveSessionAttachesToTheSameRun is the
// delivery-consumption half of B3's accept line (PRD-ISSUES-AND-ROUTINES-
// 2026 §9.4/§17, #2339):
//
//	"two comments 2s apart produce one run and two consumed deliveries"
//
// The DB-level half (the exclusivity index itself, and
// resolveSessionAndInsertAssignment's no-TOCTOU insert path) is proven
// directly against delegation_limits.go in delegation_limits_session_test.go.
// This file proves the OTHER half: that mentionRecorder — the caller of
// DispatchMention — turns a *sessionBusyError into a delivery that attaches
// to the run already in flight and is marked consumed the ordinary way
// (consumeDeliveriesForRun, assignments_run.go's finishAssignment) once that
// run finishes, rather than into a second run or a lost delivery.
//
// Uses a hand-written stub dispatcher rather than the real AssignmentHandler
// because the real one's DispatchMention races a background goroutine
// (runAssignment) that this repo's other mention tests rely on finishing
// fast (orch is nil in every test fixture, so the run fails almost
// immediately) — too fast to reliably observe an in-flight second mention
// without a flaky sleep. The stub instead deterministically reproduces the
// exact contract DispatchMention promises under B3: first call succeeds
// with a fresh id; every later call for the SAME target returns that same
// id wrapped in a *sessionBusyError, exactly as
// resolveSessionAndInsertAssignment does once the index is engaged.

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

func TestMentions_FollowUpDuringActiveSessionAttachesToTheSameRun(t *testing.T) {
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
	// mission_comment_mentions.assignment_id/claimed_by_run_id is satisfied,
	// matching what a real DispatchMention call would have written.
	execOrFatal(t, f.db, `
		INSERT INTO assignments
		    (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id)
		VALUES (?, ?, ?, ?, ?, 'first mention', 'RUNNING', 1, datetime('now'), ?)`,
		activeRunID, f.wsID, f.missionID, f.target, f.target, f.missionID)

	// Comment 1: the mentioned agent's session has no live run yet — a
	// fresh dispatch.
	f.comment(t, "first "+mentionToken("lead", f.target))
	// Comment 2, "2s later": the session's slot is already held by
	// activeRunID (per the stub's contract) — must attach, not dispatch a
	// second run.
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

	// Both deliveries claimed the SAME run.
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
	for i, r := range got {
		if r.claimedBy != activeRunID {
			t.Errorf("delivery %d (dispatch_state=%s): claimed_by_run_id = %q, want %q (the run in flight)",
				i, r.dispatchState, r.claimedBy, activeRunID)
		}
	}
	// The first delivery is the fresh dispatch; the second is B3's queued
	// outcome — distinguishable on dispatch_state, which is exactly why
	// mentionDispatchQueued exists as a value distinct from
	// mentionDispatchDispatched (an operator reading the timeline can tell
	// "started a new run" apart from "folded into the run already going").
	if got[0].dispatchState != mentionDispatchDispatched {
		t.Errorf("first delivery dispatch_state = %q, want %q", got[0].dispatchState, mentionDispatchDispatched)
	}
	if got[1].dispatchState != mentionDispatchQueued {
		t.Errorf("second delivery dispatch_state = %q, want %q", got[1].dispatchState, mentionDispatchQueued)
	}

	// Both are still 'claimed' — neither is 'consumed' yet, because the run
	// they are attached to has not finished. This is the state the ordinary
	// B2 consumption path (finishAssignment -> consumeDeliveriesForRun)
	// expects to find when the run actually completes.
	for i, r := range got {
		if r.state != "claimed" {
			t.Errorf("delivery %d state = %q, want %q before the run finishes", i, r.state, "claimed")
		}
	}

	// "two consumed deliveries": simulate the run reaching its terminal
	// status exactly the way finishAssignment does — via the same
	// consumeDeliveriesForRun this test does not want to re-implement.
	n, err := consumeDeliveriesForRun(context.Background(), f.db, activeRunID)
	if err != nil {
		t.Fatalf("consumeDeliveriesForRun: %v", err)
	}
	if n != 2 {
		t.Fatalf("consumeDeliveriesForRun consumed %d rows, want 2", n)
	}

	var consumedCount int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM mission_comment_mentions WHERE mission_id = ? AND state = 'consumed'`,
		f.missionID).Scan(&consumedCount); err != nil {
		t.Fatalf("count consumed: %v", err)
	}
	if consumedCount != 2 {
		t.Fatalf("consumed deliveries = %d, want 2 (the B3 accept line)", consumedCount)
	}
}

func (s *sessionBusyStubDispatcher) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}
