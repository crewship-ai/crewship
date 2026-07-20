package localfs

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestWatch_AfterWaitWatchersIsRejected pins the guard that keeps
// WaitWatchers' WaitGroup from being reused: once a wait has begun the
// provider is closed for new watches, so Watch must refuse rather than Add(1)
// to a WaitGroup somebody is already Wait-ing on.
func TestWatch_AfterWaitWatchersIsRejected(t *testing.T) {
	t.Parallel()
	p := tempProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); p.WaitWatchers() })

	if err := p.EnsureDir(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	events := make(chan provider.FileEvent, 8)
	if err := p.Watch(ctx, "w", events); err != nil {
		t.Fatal(err)
	}
	cancel()
	p.WaitWatchers()

	if err := p.Watch(context.Background(), "w", events); !errors.Is(err, ErrWatchersClosed) {
		t.Fatalf("Watch after WaitWatchers = %v, want ErrWatchersClosed", err)
	}
}

// TestWatch_ConcurrentWithWaitWatchers_NoPanic reproduces the crash the guard
// exists for: a Watch landing while WaitWatchers is draining panics the whole
// process with "sync: WaitGroup is reused before previous Wait has returned".
// Run with -race -count=N.
func TestWatch_ConcurrentWithWaitWatchers_NoPanic(t *testing.T) {
	t.Parallel()
	base := t.TempDir()

	for round := 0; round < 100; round++ {
		p, err := New(base)
		if err != nil {
			t.Fatal(err)
		}
		dir := "w" + strconv.Itoa(round)
		ctx, cancel := context.WithCancel(context.Background())
		if err := p.EnsureDir(ctx, dir); err != nil {
			cancel()
			t.Fatal(err)
		}
		events := make(chan provider.FileEvent, 64)
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range events {
			}
		}()
		if err := p.Watch(ctx, dir, events); err != nil {
			cancel()
			t.Fatal(err)
		}

		waited := make(chan struct{})
		go func() {
			p.WaitWatchers()
			close(waited)
		}()

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
			if err := p.Watch(lateCtx, dir, events); err != nil && !errors.Is(err, ErrWatchersClosed) {
				t.Errorf("late watch: %v", err)
			}
		}()
		wg.Wait()
		// Cancel before joining the wait: a late Watch that won the race is
		// counted, so WaitWatchers cannot return until its goroutine exits.
		lateCancel()
		<-waited
		p.WaitWatchers() // drains a late Watch that landed after the wait returned
		cancel()
		close(events)
		<-drained
	}
}
