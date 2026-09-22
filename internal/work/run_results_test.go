package work

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunProjection_RequiresFencedOutcomeAndPreservesResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   State
		stopped bool
		status  string
	}{
		{"cancelled", StateCancelled, false, "CANCELLED"},
		{"completed", StateSucceeded, false, "COMPLETED"},
		{"safe-retry", StateRetryWait, false, "FAILED"},
		{"unknown-stop", StateNeedsReconciliation, false, ""},
		{"failed-process-uncertain-effects", StateNeedsReconciliation, true, "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			ctx := t.Context()
			receipt := accept(t, s, db, backgroundReq("agent"))
			a, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Transition(ctx, TransitionRequest{WorkID: receipt.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateRunning}); err != nil {
				t.Fatal(err)
			}
			code := 1
			message := "captured failure"
			result := RunResult{ExitCode: &code, ErrorMessage: &message, Metadata: map[string]any{"total_cost_usd": 0.125, "usage": map[string]any{"input_tokens": 12}}}
			if err := s.StageRunResult(ctx, receipt.WorkID, a.RunID, a.Generation, result); err != nil {
				t.Fatal(err)
			}
			if err := s.StageRunResult(ctx, "another-work", a.RunID, a.Generation, result); !errors.Is(err, ErrNotBound) {
				t.Fatalf("cross-work stage: %v", err)
			}
			if err := s.StageRunResult(ctx, receipt.WorkID, a.RunID, a.Generation+1, result); !errors.Is(err, ErrNotBound) {
				t.Fatalf("wrong generation stage: %v", err)
			}
			if tc.state == StateCancelled {
				if _, err := s.RequestCancel(ctx, receipt.WorkID, "operator", "stop"); err != nil {
					t.Fatal(err)
				}
			}
			if ids, err := s.PendingRunProjections(ctx, 10); err != nil || len(ids) != 0 {
				t.Fatalf("capture/cancel intent projected before confirmation: %v %v", ids, err)
			}
			if err := s.Transition(ctx, TransitionRequest{WorkID: receipt.WorkID, RunID: a.RunID, Generation: a.Generation + 1, To: tc.state, StoppedRunFailed: tc.stopped}); !errors.Is(err, ErrStaleGeneration) {
				t.Fatalf("stale settlement: %v", err)
			}
			if err := s.Transition(ctx, TransitionRequest{WorkID: receipt.WorkID, RunID: a.RunID, Generation: a.Generation, To: tc.state, Reason: "provider proof", StoppedRunFailed: tc.stopped}); err != nil {
				t.Fatal(err)
			}
			// A new store instance must find the outbox without the old runtime's memory.
			s = NewStore(db)
			p, owned, err := s.RunProjection(ctx, a.RunID)
			if err != nil || !owned || p.Status != tc.status || p.Ready != (tc.status != "") {
				t.Fatalf("projection=%+v owned=%v err=%v", p, owned, err)
			}
			if !p.Ready {
				return
			}
			if p.Result.Metadata["total_cost_usd"] != 0.125 {
				t.Fatalf("lost usage: %+v", p.Result)
			}
			if tc.state == StateCancelled && p.Result.ExitCode != nil {
				t.Fatal("cancel fabricated exit code")
			}
			if err := s.StageRunResult(ctx, receipt.WorkID, a.RunID, a.Generation, RunResult{Metadata: map[string]any{"total_cost_usd": 999}}); err != nil {
				t.Fatal(err)
			}
			p, _, err = s.RunProjection(ctx, a.RunID)
			if err != nil || p.Result.Metadata["total_cost_usd"] != 0.125 {
				t.Fatalf("late duplicate overwrote result: %+v %v", p, err)
			}
			if ids, err := s.PendingRunProjections(ctx, 10); err != nil || len(ids) != 1 || ids[0] != a.RunID {
				t.Fatalf("durable outbox: %v %v", ids, err)
			}
			if err := s.MarkRunProjected(ctx, a.RunID, "not-the-status"); err != nil {
				t.Fatal(err)
			}
			if ids, _ := s.PendingRunProjections(ctx, 10); len(ids) != 1 {
				t.Fatal("wrong acknowledgement lost pending output")
			}
			if err := s.MarkRunProjected(ctx, a.RunID, p.Status); err != nil {
				t.Fatal(err)
			}
			if ids, err := s.PendingRunProjections(ctx, 10); err != nil || len(ids) != 0 {
				t.Fatalf("ack: %v %v", ids, err)
			}
		})
	}
}

func TestRunProjection_TransitionRollbackCannotPublishCancelled(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := t.Context()
	r := accept(t, s, db, backgroundReq("agent"))
	a, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StageRunResult(ctx, r.WorkID, a.RunID, a.Generation, RunResult{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_outcome BEFORE UPDATE OF run_status ON work_attempts BEGIN SELECT RAISE(ABORT,'injected write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateCancelled}); err == nil {
		t.Fatal("injected write unexpectedly succeeded")
	}
	item, err := s.Get(ctx, r.WorkID)
	if err != nil || item.State != StateStarting {
		t.Fatalf("partial work commit: %+v %v", item, err)
	}
	p, _, err := s.RunProjection(ctx, a.RunID)
	if err != nil || p.Ready {
		t.Fatalf("partial run commit: %+v %v", p, err)
	}
}

func TestRunProjection_SupersededAttemptCannotPublishLateResult(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()
	r := accept(t, s, db, backgroundReq("agent"))
	first, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "old"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseDuration + time.Second)
	if _, err := s.RecoverExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation == first.Generation {
		t.Fatal("fixture did not supersede")
	}
	if err := s.StageRunResult(ctx, r.WorkID, first.RunID, first.Generation, RunResult{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, RunID: first.RunID, Generation: first.Generation, To: StateCancelled}); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale settlement: %v", err)
	}
	if ids, err := s.PendingRunProjections(ctx, 10); err != nil || len(ids) != 0 {
		t.Fatalf("stale attempt published: %v %v", ids, err)
	}
}

func TestRunProjection_ResolutionThenLateCapture(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := t.Context()
	r := accept(t, s, db, backgroundReq("agent"))
	a, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateNeedsReconciliation}); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, r.WorkID, a.Generation, StateCancelled, "operator", "provider confirmed stopped"); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.PendingRunProjections(ctx, 10); err != nil || len(ids) != 0 {
		t.Fatalf("projected before capturing output: %v %v", ids, err)
	}
	if err := s.StageRunResult(ctx, r.WorkID, a.RunID, a.Generation, RunResult{Metadata: map[string]any{"total_cost_usd": 0.5}}); err != nil {
		t.Fatal(err)
	}
	p, owned, err := NewStore(db).RunProjection(ctx, a.RunID)
	if err != nil || !owned || !p.Ready || p.Status != "CANCELLED" || p.Result.Metadata["total_cost_usd"] != 0.5 {
		t.Fatalf("late capture/resolution: %+v %v", p, err)
	}
}

func TestRunProjection_FailedBatchCannotStarveLaterResults(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := t.Context()
	for _, agent := range []string{"first", "second"} {
		r := accept(t, s, db, backgroundReq(agent))
		a, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.StageRunResult(ctx, r.WorkID, a.RunID, a.Generation, RunResult{}); err != nil {
			t.Fatal(err)
		}
		if err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateCancelled}); err != nil {
			t.Fatal(err)
		}
		clock.Advance(time.Second)
	}
	ids, err := s.PendingRunProjections(ctx, 1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("initial batch: %v %v", ids, err)
	}
	first := ids[0]
	if err := s.MarkRunProjectionAttempt(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Simulate a failed delivery followed by a server restart.
	ids, err = NewStore(db).PendingRunProjections(ctx, 1)
	if err != nil || len(ids) != 1 || ids[0] == first {
		t.Fatalf("failed delivery starves next result: %v %v", ids, err)
	}
}
