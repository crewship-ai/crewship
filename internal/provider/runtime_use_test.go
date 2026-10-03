package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRuntimeUseRetainedChildKeepsActivationBlocked(t *testing.T) {
	var gate RuntimeUseGate
	release, err := gate.Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	use := NewRuntimeUse("crew", "runtime", release)
	child, err := use.Retain()
	if err != nil {
		t.Fatal(err)
	}
	use.Release()
	use.Release()
	if _, err := use.Retain(); !errors.Is(err, ErrRuntimeUseReleased) {
		t.Fatalf("released parent retained: %v", err)
	}
	if unlock, ok := gate.TryExclusive(); ok {
		unlock()
		t.Fatal("parent return released a still-live child")
	}
	child.Release()
	unlock, ok := gate.TryExclusive()
	if !ok {
		t.Fatal("last child release did not allow activation")
	}
	unlock()
	unlock()
}

func TestRuntimeUseCancelledAdmissionDoesNotLeak(t *testing.T) {
	var gate RuntimeUseGate
	unlock, err := gate.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if release, err := gate.Use(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("cancelled use admitted: %v", err)
	}
	if release, err := gate.Exclusive(ctx); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatalf("cancelled writer admitted: %v", err)
	}
	unlock()
	release, err := gate.Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if unlock, ok := gate.TryExclusive(); !ok {
		t.Fatal("cancelled admission left the gate busy")
	} else {
		unlock()
	}
}

func TestRuntimeUseWaitingWriterClosesNewAdmission(t *testing.T) {
	var gate RuntimeUseGate
	release, err := gate.Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	writer := make(chan func(), 1)
	go func() {
		unlock, err := gate.Exclusive(ctx)
		if err == nil {
			writer <- unlock
		}
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		gate.mu.Lock()
		waiting := gate.waitingWriters
		gate.mu.Unlock()
		if waiting > 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("writer did not register")
		case <-time.After(time.Millisecond):
		}
	}
	timed, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if extra, err := gate.Use(WithRuntimeUseNoWait(ctx)); !errors.Is(err, ErrRuntimeUseBusy) {
		if extra != nil {
			extra()
		}
		t.Fatalf("synchronous child waited or bypassed activation: %v", err)
	}
	if extra, err := gate.Use(timed); !errors.Is(err, context.DeadlineExceeded) {
		if extra != nil {
			extra()
		}
		t.Fatalf("new work bypassed pending activation: %v", err)
	}
	release()
	select {
	case unlock := <-writer:
		unlock()
	case <-ctx.Done():
		t.Fatal("writer did not acquire after drain")
	}
}

func TestRuntimeUseConcurrentRetainRelease(t *testing.T) {
	var gate RuntimeUseGate
	release, err := gate.Use(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	use := NewRuntimeUse("crew", "runtime", release)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			child, err := use.Retain()
			if err == nil {
				child.Release()
				child.Release()
			} else if !errors.Is(err, ErrRuntimeUseReleased) {
				t.Error(err)
			}
		}()
	}
	use.Release()
	wg.Wait()
	unlock, ok := gate.TryExclusive()
	if !ok {
		t.Fatal("concurrent references leaked")
	}
	unlock()
}
