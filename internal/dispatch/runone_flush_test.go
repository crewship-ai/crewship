package dispatch

import (
	"context"
	"sync"
	"testing"
)

// flushRecorder is a runtime that projects run outcomes, and records the
// state of the context each projection got, at the moment it got it.
type flushRecorder struct {
	*fakeRuntime
	mu    sync.Mutex
	calls []flushCall
}

type flushCall struct {
	err     error
	bounded bool
}

func (f *flushRecorder) FlushRunOutcomes(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, bounded := ctx.Deadline()
	f.calls = append(f.calls, flushCall{err: ctx.Err(), bounded: bounded})
	return ctx.Err()
}

// RunOne's closing flush persists what the attempt produced even when the
// caller's context is already cancelled — the same guarantee Run gives on
// shutdown. With the caller's context the projection failed with "context
// canceled" and the outcome stayed pending.
func TestRunOneFlushesOutcomesAfterCallerCancellation(t *testing.T) {
	h := newHarness(t)
	rt := &flushRecorder{fakeRuntime: h.rt}
	d := New(h.store, rt, nil, h.cfg, quiet())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _ = d.RunOne(ctx)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.calls) < 2 {
		t.Fatalf("RunOne flushed %d times, want an opening and a closing flush", len(rt.calls))
	}
	last := rt.calls[len(rt.calls)-1]
	if last.err != nil {
		t.Fatalf("closing flush got a dead context: %v", last.err)
	}
	if !last.bounded {
		t.Fatal("closing flush is unbounded; it must end within StopGrace")
	}
}
