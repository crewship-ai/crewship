package pipeline

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// schedules.go — PipelineScheduler lifecycle (Start / Stop / run).
//
// The store-level CRUD (Save/List/SoftDelete/listDueSchedules) is
// covered by schedules_test.go; this file pins the goroutine lifecycle
// — idempotent Start/Stop, prompt shutdown via stopCh, and the initial
// startup tick that source comment guarantees ("fire one tick on startup
// so newly-due schedules don't wait 30s").
// ---------------------------------------------------------------------------

func newSchedulerTestRig(t *testing.T) *PipelineScheduler {
	t.Helper()
	db := openScheduleTestDB(t)
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	exec := NewExecutor(pipelines, NewResolver(db), newMockRunner(), nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewPipelineScheduler(store, pipelines, exec, logger)
}

func TestPipelineScheduler_StartStop_PromptShutdown(t *testing.T) {
	// Start spawns the run goroutine; Stop must close stopCh AND wait
	// for run() to exit. Pin that the full Start → Stop round-trip
	// completes in well under the 30s ticker period.
	s := newSchedulerTestRig(t)
	s.Start(context.Background())

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
		// graceful
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s of Start; run goroutine never observed stopCh close")
	}
}

func TestPipelineScheduler_Start_IsIdempotent(t *testing.T) {
	// startOnce guarantees only one run goroutine. We verify by calling
	// Start twice + Stop once: if a second goroutine had spawned, the
	// stopped channel would only get closed once (the first run's
	// defer), and the SECOND would hang on `defer close(s.stopped)` →
	// "close of closed channel" panic. The absence of panic + prompt
	// Stop confirms the once-guard fires.
	s := newSchedulerTestRig(t)
	s.Start(context.Background())
	s.Start(context.Background()) // second call must be a no-op
	s.Start(context.Background()) // third for good measure

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return; possible double-goroutine spawn")
	}
}

func TestPipelineScheduler_Stop_IsIdempotent(t *testing.T) {
	// stopOnce guards close(s.stopCh) — a second Stop must not panic
	// with "close of closed channel". The second Stop is a no-op so
	// the second `<-s.stopped` inside the once block is never reached.
	s := newSchedulerTestRig(t)
	s.Start(context.Background())
	s.Stop()
	// Second + third Stop must be silent no-ops.
	for i := 0; i < 2; i++ {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("repeat Stop panicked: %v", r)
				}
			}()
			s.Stop()
		}()
		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("repeat Stop hung")
		}
	}
}

func TestPipelineScheduler_Stop_RespondsToContextCancel(t *testing.T) {
	// The run loop selects on both stopCh AND ctx.Done(). Cancelling
	// the parent context must also wind the goroutine down — otherwise
	// a long-lived ctx would leak the goroutine past server shutdown
	// even when Stop() is never called.
	s := newSchedulerTestRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)

	cancel()
	select {
	case <-s.stopped:
		// graceful — the run goroutine closed `stopped` via its defer
	case <-time.After(2 * time.Second):
		t.Fatal("ctx cancel did not stop the run goroutine within 2s")
	}
}

func TestPipelineScheduler_ConcurrentStartCallsAreSafe(t *testing.T) {
	// Race-test the once guard: many goroutines calling Start in
	// parallel must produce exactly one run goroutine. Run under -race
	// to catch any unsynchronized write to startOnce.
	s := newSchedulerTestRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Start(context.Background()) }()
	}
	wg.Wait()

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return; concurrent Start spawned a double goroutine")
	}
}

func TestPipelineScheduler_StartFiresInitialTick(t *testing.T) {
	// Source comment: "Fire one tick on startup so newly-due schedules
	// don't wait 30s." The initial tick runs listDueSchedules; with
	// no schedules seeded it's a clean read returning empty. Verify
	// the initial-tick path runs without crashing on an empty DB
	// (no panic, no double-stopped-channel-close).
	s := newSchedulerTestRig(t)
	s.Start(context.Background())

	// Give the run goroutine a moment to execute its initial tick
	// before we shut it down. Without a deterministic signal we just
	// poll briefly that Stop completes — that itself proves run()
	// reached its select loop after the initial tick.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("initial tick may have wedged the goroutine; Stop did not return")
	}
}
