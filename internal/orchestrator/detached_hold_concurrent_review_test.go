package orchestrator

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type detachedReviewPresence struct{ online atomic.Int64 }

func (p *detachedReviewPresence) Track(_ context.Context, in PresenceInput) error {
	if in.Status == "online" {
		p.online.Add(1)
	}
	return nil
}

func TestDetachedHold_ConcurrentFinalClosuresRestorePresence(t *testing.T) {
	for iteration := 0; iteration < 3000; iteration++ {
		o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
		presence := &detachedReviewPresence{}
		o.SetPresenceTracker(presence)
		req := covRunReq()
		ready := make(chan struct{})
		holds := []*detachedHold{
			{runID: "a", req: req, release: func() {}, startedAt: time.Now()},
			{runID: "b", req: req, release: func() {}, startedAt: time.Now()},
		}
		var done sync.WaitGroup
		for _, h := range holds {
			o.detached[h.runID] = h
		}
		for _, h := range holds {
			done.Add(1)
			go func() { defer done.Done(); <-ready; o.closeDetachedHold(h, true) }()
		}
		close(ready)
		done.Wait()
		if len(o.detached) != 0 {
			t.Fatal("holds not released")
		}
		if presence.online.Load() != 1 {
			t.Fatalf("iteration %d: all detached runs ended, got %d online transitions, want 1", iteration, presence.online.Load())
		}
	}
}
