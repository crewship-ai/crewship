package work

import (
	"errors"
	"testing"
	"time"
)

func TestRetainedResultRecoveryKeepsAttemptAuthority(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "succeeded"}[success], func(t *testing.T) {
			s, db, clock := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			a, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "departed"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkStarting(t.Context(), r.WorkID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
				t.Fatal(err)
			}
			code := 7
			if success {
				code = 0
			}
			result := RunResult{ExitCode: &code}
			if owned, err := s.RecoverRetainedResult(t.Context(), a.RunID, result, success); !owned || !errors.Is(err, ErrNotBound) {
				t.Fatalf("live attempt accepted: %v %v", owned, err)
			}
			clock.Advance(LeaseDuration + time.Second)
			if _, err := s.RecoverExpiredLeases(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RecoverRetainedResult(t.Context(), a.RunID, result, success); err != nil {
				t.Fatal(err)
			}
			item, err := s.Get(t.Context(), r.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			want, status := StateNeedsReconciliation, "FAILED"
			if success {
				want, status = StateSucceeded, "COMPLETED"
			}
			if item.State != want {
				t.Fatalf("work state=%s want %s", item.State, want)
			}
			p, owned, err := NewStore(db).RunProjection(t.Context(), a.RunID)
			if err != nil || !owned || !p.Ready || p.Status != status || p.Result.ExitCode == nil || *p.Result.ExitCode != code {
				t.Fatalf("projection=%+v owned=%v err=%v", p, owned, err)
			}
			// Replaying cannot change a committed outcome.
			other := 42
			if _, err := s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &other}, false); err != nil {
				t.Fatal(err)
			}
			p, _, _ = s.RunProjection(t.Context(), a.RunID)
			if *p.Result.ExitCode != code {
				t.Fatal("duplicate changed result")
			}
		})
	}
}

func TestRetainedResultRecoveryRefusesSupersededAttempt(t *testing.T) {
	s, db, clock := newTestStore(t)
	r := accept(t, s, db, backgroundReq("agent"))
	a, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "old"})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(LeaseDuration + time.Second)
	if _, err := s.RecoverExpiredLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "new"}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &zero}, true); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("superseded accepted: %v", err)
	}
	item, err := s.Get(t.Context(), r.WorkID)
	if err != nil || item.State != StateStarting {
		t.Fatalf("replacement changed: %+v %v", item, err)
	}
}

func TestRetainedResultRecoveryReplacesOnlyIncompleteObservation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "incomplete", true: "conflicting-terminal"}[conflict], func(t *testing.T) {
			s, db, _ := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			a, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "departed"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkStarting(t.Context(), r.WorkID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
				t.Fatal(err)
			}
			prior := RunResult{Metadata: map[string]any{"partial": true}}
			if conflict {
				code := 9
				prior.ExitCode = &code
			}
			if err := s.StageRunResult(t.Context(), r.WorkID, a.RunID, a.Generation, prior); err != nil {
				t.Fatal(err)
			}
			if err := s.Transition(t.Context(), TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateNeedsReconciliation, Reason: "protected host capture awaiting retained result"}); err != nil {
				t.Fatal(err)
			}
			zero := 0
			_, err = s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &zero, Metadata: map[string]any{"complete": true}}, true)
			if (err != nil) != conflict {
				t.Fatalf("conflict=%v err=%v", conflict, err)
			}
			item, err := s.Get(t.Context(), r.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			p, _, err := s.RunProjection(t.Context(), a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if conflict {
				if item.State != StateNeedsReconciliation || p.Ready {
					t.Fatalf("conflicting settlement committed: %+v %+v", item, p)
				}
			} else if item.State != StateSucceeded || !p.Ready || *p.Result.ExitCode != 0 || p.Result.Metadata["complete"] != true {
				t.Fatalf("actual result lost: %+v %+v", item, p)
			}
		})
	}
}

// A server shutdown that could not confirm the stop leaves the same question a
// lost lease does: did the execution end, and how. When the protected capture
// later answers it, recovery settles the attempt from that capture; the
// shutdown reason alone never does (#5081). Other reasons keep their owners.
func TestRetainedResultRecoveryAcceptsUnconfirmedShutdownPark(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason func(runID string) string
		accept bool
	}{
		{"stop unconfirmed", func(id string) string {
			return "server shutdown: runtime stop could not be confirmed at agent-run:" + id
		}, true},
		{"outcome not returned", func(string) string { return "server shutdown: runtime stopped but its outcome was not returned" }, true},
		{"another locator", func(string) string { return "server shutdown: runtime stop could not be confirmed at agent-run:other" }, false},
		{"operator reason", func(string) string { return "cancel requested and the runtime is still alive" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			r := accept(t, s, db, backgroundReq("agent"))
			a, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "stopped"})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.MarkStarting(t.Context(), r.WorkID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
				t.Fatal(err)
			}
			if err := s.Transition(t.Context(), TransitionRequest{WorkID: r.WorkID, RunID: a.RunID, Generation: a.Generation, To: StateNeedsReconciliation, Reason: tc.reason(a.RunID)}); err != nil {
				t.Fatal(err)
			}
			zero := 0
			_, err = s.RecoverRetainedResult(t.Context(), a.RunID, RunResult{ExitCode: &zero}, true)
			if tc.accept != (err == nil) {
				t.Fatalf("accept=%v err=%v", tc.accept, err)
			}
			if !tc.accept && !errors.Is(err, ErrNotBound) {
				t.Fatalf("refusal is not a binding refusal: %v", err)
			}
			item, err := s.Get(t.Context(), r.WorkID)
			if err != nil {
				t.Fatal(err)
			}
			want := StateNeedsReconciliation
			if tc.accept {
				want = StateSucceeded
			}
			if item.State != want {
				t.Fatalf("state=%s want %s", item.State, want)
			}
		})
	}
}
