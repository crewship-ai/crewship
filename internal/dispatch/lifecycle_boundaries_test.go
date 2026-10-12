package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

func TestRunOneCompletesOneAttemptAndLeavesNextQueued(t *testing.T) {
	h := newHarness(t)
	first := h.accept("first")
	second := h.accept("second")
	d := New(h.store, h.rt, nil, h.cfg, nil)
	// Wake-up hints are optional and may be dropped under load.
	done := make(chan struct{})
	go func() {
		for range 1024 {
			d.Hint(first.WorkID)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("hint blocked acceptance")
	}
	worked, err := d.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("run one=%v %v", worked, err)
	}
	h.waitForState(first.WorkID, work.StateSucceeded)
	h.waitForState(second.WorkID, work.StateQueued)
	if h.rt.starts.Load() != 1 {
		t.Fatalf("started %d runtimes", h.rt.starts.Load())
	}
	worked, err = d.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("second=%v %v", worked, err)
	}
	h.waitForState(second.WorkID, work.StateSucceeded)
	worked, err = d.RunOne(t.Context())
	if err != nil || worked {
		t.Fatalf("empty queue=%v %v", worked, err)
	}
}

func TestRunOneRefusesUnconfiguredOrUnreadableLedger(t *testing.T) {
	h := newHarness(t)
	h.accept("unclaimed")
	cfg := Config{}
	d := New(h.store, h.rt, nil, cfg, nil)
	if ok, err := d.RunOne(t.Context()); ok || !errors.Is(err, ErrNoExecutableKinds) {
		t.Fatalf("unconfigured=%v %v", ok, err)
	}
	d.cfg.Kinds = h.cfg.Kinds
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.RunOne(t.Context()); ok || err == nil || !strings.Contains(err.Error(), "recover expired leases") {
		t.Fatalf("unreadable=%v %v", ok, err)
	}
	if err := d.Run(t.Context()); err == nil {
		t.Fatal("loop started without recovery")
	}
	if h.rt.starts.Load() != 0 {
		t.Fatal("runtime started without a readable ledger")
	}
}

type stopResponse struct {
	stopped bool
	err     error
}
type boundaryRuntime struct {
	*fakeRuntime
	stops     []stopResponse
	stopCalls int
	alive     bool
	probeErr  error
	probes    int
}

func (r *boundaryRuntime) Stop(context.Context, string) (bool, error) {
	i := r.stopCalls
	r.stopCalls++
	if i >= len(r.stops) {
		return false, errors.New("stop unavailable")
	}
	return r.stops[i].stopped, r.stops[i].err
}
func (r *boundaryRuntime) Alive(context.Context, string) (bool, error) {
	r.probes++
	return r.alive, r.probeErr
}

func TestDetachedStopRequiresConfirmationAfterEscalation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []stopResponse
		cancel  bool
		wantErr string
		calls   int
	}{
		{"initial failure", []stopResponse{{err: errors.New("offline")}}, false, "signal: offline", 1},
		{"escalation failure", []stopResponse{{}, {err: errors.New("offline")}}, false, "escalated signal: offline", 2},
		{"escalation succeeds", []stopResponse{{}, {stopped: true}}, false, "", 2},
		{"ignores both signals", []stopResponse{{}, {}}, false, "ignored the escalated stop signal", 2},
		{"canceled grace", []stopResponse{{}}, true, "context canceled", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &boundaryRuntime{fakeRuntime: newFakeRuntime(), stops: tc.replies}
			d := New(nil, rt, nil, Config{StopGrace: time.Millisecond}, quiet())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := d.stopDetachedConfirmed(ctx, &liveAttempt{locator: "owned-runtime"})
			if (err == nil) != (tc.wantErr == "") || err != nil && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("stop=%v", err)
			}
			if rt.stopCalls != tc.calls {
				t.Fatalf("signals=%d want %d", rt.stopCalls, tc.calls)
			}
		})
	}
}

func TestDetachedLifecyclePreservesUnknownOutcome(t *testing.T) {
	for _, boundary := range []string{"broken probes", "abandoned", "shutdown", "canceled observer", "superseded unstoppable"} {
		t.Run(boundary, func(t *testing.T) {
			h := newHarness(t)
			receipt := h.accept(boundary)
			claim, err := h.store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			a := Assignment{Item: claim.Item, RunID: claim.RunID, Attempt: claim.Attempt, Generation: claim.Generation}
			rt := &boundaryRuntime{fakeRuntime: h.rt, alive: true}
			rt.classifyAs = OutcomeDetached
			d := New(h.store, rt, nil, Config{ConfirmPollInterval: time.Millisecond, StopGrace: time.Second}, quiet())
			live := &liveAttempt{assignment: a, locator: rt.Locator(a), cancel: func() {}, shutdown: make(chan struct{})}
			if err := h.store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, live.locator); err != nil {
				t.Fatal(err)
			}
			superseded, abandoned := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			want := work.StateNeedsReconciliation
			switch boundary {
			case "broken probes":
				rt.probeErr = errors.New("daemon unavailable")
			case "abandoned":
				close(abandoned)
			case "shutdown":
				rt.stops = []stopResponse{{stopped: true}}
				close(live.shutdown)
			case "canceled observer":
				cancel()
				want = work.StateStarting
			case "superseded unstoppable":
				rt.stops = []stopResponse{{}, {}}
				close(superseded)
				want = work.StateStarting
			}
			d.awaitDetachedThenSettle(ctx, live, superseded, abandoned, make(chan error), errors.New("detached"))
			item, err := h.store.Get(t.Context(), receipt.WorkID)
			if err != nil || item.State != want {
				t.Fatalf("outcome=%+v %v; want %s", item, err, want)
			}
			if boundary == "broken probes" && (rt.probes != 5 || rt.stopCalls != 1) {
				t.Fatalf("failed probes=%d signals=%d", rt.probes, rt.stopCalls)
			}
			if boundary == "superseded unstoppable" && rt.stopCalls != 2 {
				t.Fatalf("superseded signals=%d", rt.stopCalls)
			}
			if h.rt.starts.Load() != 0 {
				t.Fatal("replayed detached work")
			}
		})
	}
}

func TestRuntimeOutcomeNamesRemainDistinctAndUnknownIsUnclear(t *testing.T) {
	for outcome, want := range map[Outcome]string{OutcomeUnclear: "unclear", OutcomeSucceeded: "succeeded", OutcomeFailed: "failed", OutcomeRetryable: "retryable", OutcomeDetached: "detached", Outcome(99): "unclear"} {
		if got := outcome.String(); got != want {
			t.Errorf("outcome %d: %q want %q", outcome, got, want)
		}
	}
	if decision := NotYet("approval pending", 0); decision.retryAfter <= 0 || decision.kind != decisionNotYet {
		t.Fatalf("hold became hot loop: %+v", decision)
	}
}

func TestRunOneDefersHeldWorkWithoutSpendingAttempt(t *testing.T) {
	h := newHarness(t)
	receipt := h.accept("held")
	d := New(h.store, h.rt, AuthorizerFunc(func(context.Context, Assignment) (Decision, error) { return NotYet("approval pending", 0), nil }), h.cfg, quiet())
	before := time.Now()
	worked, err := d.RunOne(t.Context())
	if err != nil || !worked {
		t.Fatalf("defer=%v %v", worked, err)
	}
	item, err := h.store.Get(t.Context(), receipt.WorkID)
	if err != nil || item.State != work.StateQueued || item.Attempts != 0 || item.EligibleAt.Before(before.Add(defaultNotYetRetry)) {
		t.Fatalf("held work=%+v %v", item, err)
	}
	if h.rt.starts.Load() != 0 {
		t.Fatal("held work started a runtime")
	}
}

func TestDispatcherNeverStartsWithoutDurableIntent(t *testing.T) {
	h := newHarness(t)
	receipt := h.accept("intent-write-fails")
	if _, err := h.db.Exec(`CREATE TRIGGER reject_intent BEFORE UPDATE OF runtime_phase ON work_attempts WHEN NEW.runtime_phase='starting' BEGIN SELECT RAISE(ABORT,'intent unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	d := New(h.store, h.rt, nil, h.cfg, quiet())
	if worked, err := d.RunOne(t.Context()); err != nil || !worked {
		t.Fatalf("claim=%v %v", worked, err)
	}
	item, err := h.store.Get(t.Context(), receipt.WorkID)
	if err != nil || item.State != work.StateNeedsReconciliation || !strings.Contains(item.StateReason, "start intent could not be recorded") {
		t.Fatalf("lost intent=%+v %v", item, err)
	}
	if h.rt.starts.Load() != 0 {
		t.Fatal("runtime created before durable intent")
	}
}
