package dispatch

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

// A paused dispatcher (an instance restore's "queue" hold, a backup's quiet
// window) claims nothing: the work stays queued with its attempt budget
// untouched, and runs once the pause lifts.
func TestVertical_APausedDispatcherLeavesWorkQueued(t *testing.T) {
	h := newHarness(t)
	r := h.accept("dlv-paused")
	var paused atomic.Bool
	paused.Store(true)
	h.cfg.Paused = paused.Load
	_, stop := h.runDispatcher(nil)
	defer stop()

	time.Sleep(4 * h.cfg.PollInterval)
	if got := h.rt.starts.Load(); got != 0 {
		t.Fatalf("%d runtimes started while paused", got)
	}
	it, _ := h.store.Get(context.Background(), r.WorkID)
	if it.State != work.StateQueued {
		t.Fatalf("state while paused = %q, want queued", it.State)
	}
	paused.Store(false)
	h.waitForState(r.WorkID, work.StateSucceeded)
}
