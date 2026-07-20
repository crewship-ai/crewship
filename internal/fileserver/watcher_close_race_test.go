package fileserver

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
)

// TestWatch_AfterCloseIsRejected pins the guard that keeps Close's WaitGroup
// from being reused: once Close has begun, Watch must refuse rather than
// Add(1) to a WaitGroup somebody is already Wait-ing on.
func TestWatch_AfterCloseIsRejected(t *testing.T) {
	base := t.TempDir()
	w := NewWatcher(base, discardLogger(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Watch(ctx, "crew-live"); err != nil {
		t.Fatalf("watch: %v", err)
	}
	cancel()
	w.Close()

	if err := w.Watch(context.Background(), "crew-late"); !errors.Is(err, ErrWatcherClosed) {
		t.Fatalf("Watch after Close = %v, want ErrWatcherClosed", err)
	}
}

// TestWatch_ConcurrentWithClose_NoPanic reproduces the crash the guard exists
// for: a Watch landing while Close is draining panics the whole process with
// "sync: WaitGroup is reused before previous Wait has returned". The window is
// the moment the last watch goroutine exits while Close is parked in Wait, so
// the test drives that exact shape repeatedly. Run with -race -count=N.
func TestWatch_ConcurrentWithClose_NoPanic(t *testing.T) {
	base := t.TempDir()

	for round := 0; round < 100; round++ {
		w := NewWatcher(base, discardLogger(), nil)

		ctx, cancel := context.WithCancel(context.Background())
		if err := w.Watch(ctx, "crew-"+strconv.Itoa(round)); err != nil {
			cancel()
			t.Fatalf("watch: %v", err)
		}

		// Close parks in Wait with the counter at 1.
		closed := make(chan struct{})
		go func() {
			w.Close()
			close(closed)
		}()

		// Now drop the counter to 0 and Add to it from another goroutine at
		// the same time — that is the reuse the panic complains about.
		lateCtx, lateCancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancel()
		}()
		go func() {
			defer wg.Done()
			// Either outcome is fine; a panic is not.
			if err := w.Watch(lateCtx, "crew-late"); err != nil && !errors.Is(err, ErrWatcherClosed) {
				t.Errorf("late watch: %v", err)
			}
		}()
		wg.Wait()
		// Cancel before joining Close: a late Watch that won the race is
		// counted, so Close cannot return until its goroutine exits.
		lateCancel()
		<-closed
		w.Close() // drains a late Watch that landed after Close returned
		cancel()
	}
}
