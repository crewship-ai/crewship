package scheduler

// #1668 — internal/scheduler had no concurrency bound of any kind.
// robfig/cron runs every due entry in its own goroutine, so twenty crews
// sharing a cron minute were twenty simultaneous EnsureCrewRuntime calls,
// each also holding a 45-minute context and a chat.

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAcquireDispatchSlot_BoundsSimultaneousFires(t *testing.T) {
	const cap, n = 3, 20
	s := &Scheduler{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	s.initDispatchBound(cap)

	var mu sync.Mutex
	var inFlight, peak int

	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := s.acquireDispatchSlot()
			if !ok {
				t.Error("acquireDispatchSlot returned !ok on a live scheduler")
				return
			}
			defer release()
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			<-gate
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()
	}

	time.Sleep(60 * time.Millisecond)
	close(gate)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > cap {
		t.Fatalf("peak concurrent scheduled dispatches = %d, exceeds cap %d — the scheduler is unbounded", peak, cap)
	}
	if peak == 0 {
		t.Fatal("nothing was dispatched")
	}
}

// A shutting-down scheduler must not park a cron goroutine forever waiting
// for a slot that is never coming.
func TestAcquireDispatchSlot_ShutdownDoesNotBlockForever(t *testing.T) {
	s := &Scheduler{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.initDispatchBound(1)

	held, ok := s.acquireDispatchSlot()
	if !ok {
		t.Fatal("first acquire failed")
	}
	defer held()

	done := make(chan bool, 1)
	go func() {
		_, ok := s.acquireDispatchSlot()
		done <- ok
	}()

	// Nothing should get through while the only slot is held...
	select {
	case <-done:
		t.Fatal("a second dispatch was admitted while the only slot was held")
	case <-time.After(50 * time.Millisecond):
	}

	s.cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("acquire reported success after shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cron goroutine stayed parked on the dispatch bound after shutdown")
	}
}

// Release must be idempotent for the same reason the admission controller's
// is: it is deferred on paths that also return early.
func TestAcquireDispatchSlot_ReleaseIsIdempotent(t *testing.T) {
	s := &Scheduler{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()
	s.initDispatchBound(1)

	release, ok := s.acquireDispatchSlot()
	if !ok {
		t.Fatal("acquire failed")
	}
	release()
	release()
	release()

	// Exactly one slot must exist afterwards, not three.
	r1, ok := s.acquireDispatchSlot()
	if !ok {
		t.Fatal("re-acquire failed")
	}
	defer r1()

	done := make(chan struct{})
	go func() {
		r2, ok2 := s.acquireDispatchSlot()
		if ok2 {
			r2()
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("repeated release handed out a phantom dispatch slot")
	case <-time.After(50 * time.Millisecond):
	}
	s.cancel()
	<-done
}

// A Scheduler built without initDispatchBound (any construction path that
// predates this, and every test that builds one by hand) must not deadlock.
func TestAcquireDispatchSlot_NilSemaphore_IsPassThrough(t *testing.T) {
	s := &Scheduler{}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	defer s.cancel()

	for i := 0; i < 50; i++ {
		release, ok := s.acquireDispatchSlot()
		if !ok {
			t.Fatal("unbounded scheduler refused a dispatch")
		}
		release()
	}
}

func TestNew_InstallsADispatchBound(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil, Config{}, testLogger())
	defer s.cancel()
	if s.dispatchSem == nil {
		t.Fatal("New built a Scheduler with no dispatch bound at all")
	}
	if got := cap(s.dispatchSem); got != defaultMaxConcurrentDispatches {
		t.Errorf("dispatch bound = %d, want the default %d", got, defaultMaxConcurrentDispatches)
	}
}

func TestNew_HonoursConfiguredDispatchBound(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil, Config{MaxConcurrentDispatches: 2}, testLogger())
	defer s.cancel()
	if got := cap(s.dispatchSem); got != 2 {
		t.Errorf("dispatch bound = %d, want the configured 2", got)
	}
}
