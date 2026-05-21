package scheduler

// CodeRabbit regressions on PR-C (PR #470).
//
// Pins the scheduler panic-recovery fix: robfig/cron/v3 does NOT recover
// panics by default, so a panic in a registered RegisterPlatformRoutine
// fn would crash the entire process. The wrapper inside AddFunc must
// recover and log instead.

import (
	"context"
	"testing"
	"time"
)

// TestCR_RegisterPlatformRoutine_RecoversPanic registers a routine that
// panics on every fire, lets it fire once via the @every 1s cron, and
// asserts the process survives + a subsequent fn (or the same one)
// completes. Without the defer recover() wrapper inside AddFunc, the
// cron worker goroutine would tear down the test process with a panic
// stack trace instead of completing.
func TestCR_RegisterPlatformRoutine_RecoversPanic(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())

	var (
		panickerFired = make(chan struct{}, 8)
		safeFired     = make(chan struct{}, 8)
	)

	// First routine panics every fire. If the panic propagates instead of
	// being recovered, the cron worker goroutine tears down → the safe
	// routine below never fires either, the second receive blocks, and
	// the test times out (the load-bearing assertion).
	if err := s.RegisterPlatformRoutine("cr_panic", "@every 1s", func(ctx context.Context) {
		select {
		case panickerFired <- struct{}{}:
		default:
		}
		panic("CR regression: this panic must be recovered, not crash the process")
	}); err != nil {
		t.Fatalf("RegisterPlatformRoutine(panicker): %v", err)
	}
	if err := s.RegisterPlatformRoutine("cr_safe", "@every 1s", func(ctx context.Context) {
		select {
		case safeFired <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("RegisterPlatformRoutine(safe): %v", err)
	}

	s.c.Start()
	defer s.Stop()

	// Wait for both routines to fire at least once. Order is
	// deterministic for cron's serial worker (entries fire in the order
	// they were registered for the same minute), so the panicker
	// goroutine completes (with its recover) before the safe one starts.
	deadline := time.After(5 * time.Second)
	for _, ch := range []chan struct{}{panickerFired, safeFired} {
		select {
		case <-ch:
		case <-deadline:
			t.Fatal("routine never fired — panic in sibling likely killed the cron loop")
		}
	}
}

// TestCR_RegisterPlatformRoutine_RejectsBadCron pins the existing
// validation path so the panic-recovery wrapper above doesn't shadow it.
func TestCR_RegisterPlatformRoutine_RejectsBadCron(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())
	defer s.Stop()

	err := s.RegisterPlatformRoutine("cr_bad", "this is not a cron", func(context.Context) {})
	if err == nil {
		t.Fatal("expected error on invalid cron expression")
	}
}
