//go:build linux

package restrictedworkflow

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

func TestWorkflowDispatcherRecoversAfterInitialRecoveryFailure(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	failed := make(chan struct{})
	allowDispatch := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		runWorkflowDispatcher(ctx, func(ctx context.Context) error {
			if calls.Add(1) == 1 {
				close(failed)
				return errors.New("transient recovery failure")
			}
			select {
			case <-allowDispatch:
			case <-ctx.Done():
				return ctx.Err()
			}
			return s.dispatcher.Run(ctx)
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	<-failed
	item, err := s.ledger.Get(t.Context(), receipt.ID)
	if err != nil || item.State != work.StateQueued || item.Attempts != 0 || item.Generation != 0 {
		t.Fatalf("failed recovery claimed private work: %+v %v", item, err)
	}
	close(allowDispatch)
	// A bounded liveness check, not a latency benchmark: the same 30 s budget
	// as the package's other dispatch waits. Race builds share the host with
	// the full backend suite; 10 s failed CI with the work still running.
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		result, err := s.Result(t.Context(), "h1", "w", receipt.ID)
		if err == nil && result.State == "completed" && len(result.Outputs) == 2 {
			if calls.Load() != 2 {
				t.Fatalf("dispatcher retry count: %d", calls.Load())
			}
			return
		}
		select {
		case <-done:
			t.Fatal("dispatcher stopped permanently after recovery failure")
		case <-deadline.C:
			t.Fatalf("private work did not resume: %+v %v", result, err)
		case <-tick.C:
		}
	}
}

func TestWorkflowDispatcherStopsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorkflowDispatcher(ctx, func(context.Context) error {
			called <- struct{}{}
			return errors.New("transient recovery failure")
		})
	}()
	<-called
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt backoff")
	}
	if len(called) != 0 {
		t.Fatal("dispatcher retried after cancellation")
	}
}

func TestWorkflowDispatcherDoesNotRetryInvalidConfiguration(t *testing.T) {
	s, _ := fixture(t)
	invalid := dispatch.New(s.ledger, s.runtime, s.runtime, dispatch.Config{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWorkflowDispatcher(ctx, invalid.Run)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unsafe dispatcher configuration was retried")
	}
}
