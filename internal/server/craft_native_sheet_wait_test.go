package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTestBusy = errors.New("busy")

func isTestBusy(err error) bool { return errors.Is(err, errTestBusy) }

// A third Sheet waits in turn for a session slot instead of failing at once;
// it is answered busy only after the wait, or as soon as its run ends.
func TestASheetChangeWaitsForAFreeSessionSlot(t *testing.T) {
	calls := 0
	err := waitForSlot(context.Background(), time.Second, time.Millisecond, isTestBusy, func() error {
		calls++
		if calls < 3 {
			return errTestBusy
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("did not wait for the freed slot: %v after %d tries", err, calls)
	}

	start := time.Now()
	err = waitForSlot(context.Background(), 30*time.Millisecond, time.Millisecond, isTestBusy, func() error { return errTestBusy })
	if !isTestBusy(err) || time.Since(start) < 30*time.Millisecond {
		t.Fatalf("gave up before the wait: %v after %s", err, time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	err = waitForSlot(ctx, time.Minute, time.Millisecond, isTestBusy, func() error { calls++; return errTestBusy })
	if !isTestBusy(err) || calls != 1 {
		t.Fatalf("an ended run kept waiting: %v after %d tries", err, calls)
	}

	// A short run (a native task's engine window) waits only a third of what
	// it has left, so the change itself still fits.
	short, stop := context.WithTimeout(context.Background(), 90*time.Millisecond)
	defer stop()
	start = time.Now()
	_ = waitForSlot(short, time.Minute, time.Millisecond, isTestBusy, func() error { return errTestBusy })
	if took := time.Since(start); took > 60*time.Millisecond {
		t.Fatalf("waited %s of a 90ms run", took)
	}

	other := errors.New("engine failed")
	calls = 0
	if err := waitForSlot(context.Background(), time.Minute, time.Millisecond, isTestBusy, func() error { calls++; return other }); err != other || calls != 1 {
		t.Fatalf("a real failure was retried: %v after %d tries", err, calls)
	}
}
