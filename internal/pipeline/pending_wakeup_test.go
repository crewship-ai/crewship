package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestPendingWakeupInterruptsBootDelay(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	executor := &fakeExecutor{}
	dispatcher := NewPendingRunDispatcher(NewPendingRunStore(db), executor, nil)
	dispatcher.tick = time.Hour
	var paused atomic.Bool
	paused.Store(true)
	bootSweep := make(chan struct{}, 1)
	dispatcher.SetPaused(func() bool {
		if paused.Load() {
			select {
			case bootSweep <- struct{}{}:
			default:
			}
			return true
		}
		return false
	})
	ctx, cancel := context.WithCancel(t.Context())
	dispatcher.Start(ctx)
	defer func() { cancel(); dispatcher.Stop() }()
	select {
	case <-bootSweep:
	case <-time.After(5 * time.Second):
		t.Fatal("boot sweep did not run")
	}
	paused.Store(false)
	// A separately constructed producer must wake the same dispatcher while
	// its initial hour-long jitter/timer has not yet elapsed.
	_, _, err := NewPendingRunStore(db).Enqueue(t.Context(), PendingRun{ID: "wake", WorkspaceID: "w", PipelineID: "p", PipelineSlug: "s", FireAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if atomic.LoadInt32(&executor.completed) == 1 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("enqueue waited for polling instead of waking")
		case <-poll.C:
		}
	}
}
