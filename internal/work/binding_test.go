package work

import (
	"context"
	"errors"
	"testing"
	"time"
)

// These are the three defects the 2026-09-11 review reproduced, kept as
// permanent regression tests. The originals are preserved in
// docs/prd/reports/codex-review-work-regressions-2026-09-11.go.txt; these are
// the same scenarios written to this package's conventions and extended with
// the acceptance cases the review named.
//
// What they have in common is the thing that was actually wrong: the store
// treated "generation matches" as identity. It is not. Generation is a counter
// per work item, so two items claimed once each both sit at 1, and a fence
// built on it alone lets any live worker act on any other work item.

// R1. A cancel that read `queued` and then asked for `cancelled` with no
// precondition could land after a claim, turning a live run's row terminal
// while its process carried on — and the API said so in as many words.
func TestCancel_DoesNotDeclareALiveRuntimeStopped(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))

	// The handler's read happens here, when the work is still queued.
	before, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.State != StateQueued {
		t.Fatalf("state = %q, want queued", before.State)
	}

	// A worker claims and reaches a live runtime in the window.
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "crew-1/tmux:live"); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := s.StartRunning(ctx, r.WorkID, c.RunID, c.Generation, "crew-1/tmux:live"); err != nil {
		t.Fatalf("start running: %v", err)
	}

	// The cancel now arrives, carrying the handler's stale view of the world.
	res, err := s.RequestCancel(ctx, r.WorkID, "operator", "user pressed stop")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Outcome != CancelOutcomeRequested {
		t.Errorf("outcome = %q, want %q — nothing stopped the runtime, so nothing may claim it stopped",
			res.Outcome, CancelOutcomeRequested)
	}
	if res.State != StateRunning {
		t.Errorf("state = %q, want running", res.State)
	}
	if res.RunID != c.RunID || res.Generation != c.Generation {
		t.Errorf("request recorded against run %q gen %d, want %q gen %d — a request stamped with a "+
			"superseded attempt lets a replaced worker satisfy a cancel meant for its replacement",
			res.RunID, res.Generation, c.RunID, c.Generation)
	}

	// The work is still running, and still holds its capacity.
	it, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateRunning {
		t.Fatalf("state after cancel = %q, want running", it.State)
	}

	// The request is durable: a restart must find it, because the dispatcher
	// that has to act on it may not be the process that took the call.
	requested, err := s.CancelRequested(ctx, c.RunID)
	if err != nil {
		t.Fatalf("cancel requested: %v", err)
	}
	if !requested {
		t.Error("the cancel request did not survive as durable state; a restart would forget the user asked")
	}

	// And only a worker confirming the stop produces `cancelled`.
	if err := s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation,
		To: StateCancelled, Reason: "runtime confirmed stopped",
	}); err != nil {
		t.Fatalf("worker confirms the stop: %v", err)
	}
	if it, _ = s.Get(ctx, r.WorkID); it.State != StateCancelled {
		t.Errorf("state = %q, want cancelled once the worker confirmed it", it.State)
	}
}

// R1, the other half: work that genuinely has not started stops outright, and
// that is the ONLY case that may answer `cancelled`.
func TestCancel_QueuedWorkStopsOutrightAndIsIdempotent(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))

	res, err := s.RequestCancel(ctx, r.WorkID, "operator", "not needed after all")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Outcome != CancelOutcomeCancelled || res.State != StateCancelled {
		t.Fatalf("outcome %q state %q, want cancelled/cancelled", res.Outcome, res.State)
	}

	// Asking again is not a state change.
	again, err := s.RequestCancel(ctx, r.WorkID, "operator", "pressed twice")
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if again.Outcome != CancelOutcomeAlreadyTerminal || again.State != StateCancelled {
		t.Errorf("second cancel = %q/%q, want already_terminal/cancelled", again.Outcome, again.State)
	}

	// And cancelled work is not claimable.
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); !errors.Is(err, ErrNoWork) {
		t.Errorf("claim after cancel = %v, want ErrNoWork", err)
	}
}

// R1 again: a completion that wins the race is reported as what it is.
func TestCancel_ReportsTheRealStateWhenCompletionWins(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	mustTransition(t, s, r.WorkID, c, StateRunning)
	mustTransition(t, s, r.WorkID, c, StateSucceeded)

	res, err := s.RequestCancel(ctx, r.WorkID, "operator", "too late")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if res.Outcome != CancelOutcomeAlreadyTerminal || res.State != StateSucceeded {
		t.Errorf("outcome %q state %q, want already_terminal/succeeded — telling someone "+
			"'cancelled' about work that succeeded is the one direction that matters",
			res.Outcome, res.State)
	}
}

// R2. Two work items claimed once each are both at generation 1, so a fence
// built on generation alone lets run B report a result for work A — and the
// follow-up UPDATE then closed B's own attempt, losing the live run too.
func TestTransition_RefusesARunFromAnotherWorkItem(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	a := accept(t, s, db, backgroundReq("agent-a"))
	b := accept(t, s, db, backgroundReq("agent-b"))

	ca, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker", AgentID: "agent-a"})
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	cb, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker", AgentID: "agent-b"})
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if ca.Generation != cb.Generation {
		t.Fatalf("generations %d and %d differ; this test is only meaningful when they collide",
			ca.Generation, cb.Generation)
	}
	if err := s.StartRunning(ctx, a.WorkID, ca.RunID, ca.Generation, "a-runtime"); err != nil {
		t.Fatalf("start A: %v", err)
	}

	err = s.Transition(ctx, TransitionRequest{
		WorkID: a.WorkID, RunID: cb.RunID, Generation: ca.Generation, To: StateSucceeded,
	})
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("run B completing work A = %v, want ErrNotBound", err)
	}

	// The refusal changed nothing: not A, not B, not either attempt.
	itA, _ := s.Get(ctx, a.WorkID)
	itB, _ := s.Get(ctx, b.WorkID)
	if itA.State != StateRunning {
		t.Errorf("work A = %q, want running", itA.State)
	}
	if itB.State != StateStarting {
		t.Errorf("work B = %q, want starting — the refusal must not have closed B's attempt", itB.State)
	}
	var openAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_attempts WHERE ended_at IS NULL`).Scan(&openAttempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if openAttempts != 2 {
		t.Errorf("%d open attempts, want 2 — a refused transition closed one", openAttempts)
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_events WHERE work_id = ?`, a.WorkID).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 3 { // accepted, claimed, running
		t.Errorf("work A has %d events, want 3 — a refused transition wrote history", events)
	}
}

// R2. StartRunning ignored how many rows its locator UPDATE touched, so a run
// id that existed nowhere still moved the work item to `running` — work marked
// live with no attempt behind it.
func TestStartRunning_RefusesARunThatDoesNotExist(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	err = s.StartRunning(ctx, r.WorkID, "nonexistent-run", c.Generation, "locator")
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("unknown run = %v, want ErrNotBound", err)
	}
	it, _ := s.Get(ctx, r.WorkID)
	if it.State != StateStarting {
		t.Errorf("state = %q, want starting — an unknown run must not move the work", it.State)
	}
}

// R2, the rest of the acceptance list: an ended attempt, a zero generation, and
// a late completion all have to be refused, and refused without side effects.
func TestTransition_RefusesEveryUnboundShape(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	tests := []struct {
		name string
		req  TransitionRequest
		want error
	}{
		{
			name: "no run id at all",
			req:  TransitionRequest{WorkID: r.WorkID, Generation: c.Generation, To: StateRunning},
			want: ErrNotBound,
		},
		{
			name: "zero generation, which used to switch the fence off",
			req:  TransitionRequest{WorkID: r.WorkID, RunID: c.RunID, To: StateRunning},
			want: ErrNotBound,
		},
		{
			name: "a run that does not exist",
			req:  TransitionRequest{WorkID: r.WorkID, RunID: "ghost", Generation: c.Generation, To: StateRunning},
			want: ErrNotBound,
		},
		{
			name: "the right run, the wrong generation",
			req:  TransitionRequest{WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation + 5, To: StateRunning},
			want: ErrStaleGeneration,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Transition(ctx, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			it, _ := s.Get(ctx, r.WorkID)
			if it.State != StateStarting {
				t.Errorf("state = %q, want starting — a refused transition changed the work", it.State)
			}
		})
	}

	// An attempt that has already ended cannot report anything more. Take it
	// through a real lease loss so the attempt is closed the way production
	// closes it.
	clock.Advance(LeaseDuration + time.Second)
	if _, err := s.RecoverExpiredLeases(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	err = s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateSucceeded,
	})
	if !errors.Is(err, ErrNotBound) && !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("late completion from an ended attempt = %v, want a binding or staleness refusal", err)
	}
}

// R4. Recovery used to read "no locator" as proof that no process had been
// created, which cannot tell "never started" from "started, and we died before
// writing it down". The phase says which.
func TestRecovery_OnlyAPlannedAttemptIsSafeToRequeue(t *testing.T) {
	tests := []struct {
		name         string
		markStarting bool
		wantRequeued bool
	}{
		{name: "never left planned, so nothing external exists", markStarting: false, wantRequeued: true},
		{name: "a start was requested, so a runtime may exist", markStarting: true, wantRequeued: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, db, clock := newTestStore(t)
			ctx := context.Background()
			r := accept(t, s, db, backgroundReq("agent-jamie"))
			c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
			if err != nil {
				t.Fatalf("claim: %v", err)
			}
			if tc.markStarting {
				// The locator is written BEFORE the process exists, which is
				// the whole point: recovery can then look for a runtime by an
				// identity it knew in advance.
				if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "crew-1/tmux:agent-jamie-"+c.RunID); err != nil {
					t.Fatalf("mark starting: %v", err)
				}
			}

			clock.Advance(LeaseDuration + time.Second)
			out, err := s.RecoverExpiredLeases(ctx)
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if tc.wantRequeued {
				if len(out.Requeued) != 1 {
					t.Fatalf("requeued = %v, reconciliation = %v; want it requeued", out.Requeued, out.Reconciliation)
				}
				return
			}
			if len(out.Reconciliation) != 1 {
				t.Fatalf("requeued = %v, reconciliation = %v; want it parked for reconciliation — "+
					"a start was requested, so a process may be running", out.Requeued, out.Reconciliation)
			}
			it, _ := s.Get(ctx, r.WorkID)
			if it.State != StateNeedsReconciliation {
				t.Errorf("state = %q, want needs_reconciliation", it.State)
			}
		})
	}
}

// R4. MarkStarting is itself bound, and it is a one-shot: a second start intent
// for the same attempt is a bug in the dispatcher, not a retry.
func TestMarkStarting_IsBoundAndHappensOnce(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent-jamie"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	if err := s.MarkStarting(ctx, r.WorkID, "ghost", c.Generation, "loc"); !errors.Is(err, ErrNotBound) {
		t.Errorf("unknown run = %v, want ErrNotBound", err)
	}
	if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, ""); !errors.Is(err, ErrNotBound) {
		t.Errorf("empty locator = %v, want ErrNotBound — an intent with no locator tells recovery nothing", err)
	}
	if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "loc"); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := s.MarkStarting(ctx, r.WorkID, c.RunID, c.Generation, "loc-2"); !errors.Is(err, ErrNotBound) {
		t.Errorf("second start intent = %v, want ErrNotBound", err)
	}
}
