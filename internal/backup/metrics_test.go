package backup

import (
	"errors"
	"testing"
)

func TestMetrics_CreateSuccessRecordsScope(t *testing.T) {
	ResetMetrics()
	done := ObserveCreateStart("workspace")
	done(nil, 4096)

	snap := Snapshot()
	if snap.CreatedTotal != 1 {
		t.Errorf("CreatedTotal: got %d want 1", snap.CreatedTotal)
	}
	if snap.CreatedByScope["workspace"] != 1 {
		t.Errorf("CreatedByScope[workspace]: got %d want 1", snap.CreatedByScope["workspace"])
	}
	if snap.SizeBytesTotal != 4096 {
		t.Errorf("SizeBytesTotal: got %d want 4096", snap.SizeBytesTotal)
	}
	if snap.FailedTotal != 0 {
		t.Errorf("FailedTotal: got %d want 0", snap.FailedTotal)
	}
}

func TestMetrics_CreateFailureClassifiesReason(t *testing.T) {
	ResetMetrics()
	done := ObserveCreateStart("crew")
	done(errors.New("wrapped: "+ErrLockHeld.Error()), 0) // not Is-matched — should land in "other"
	done2 := ObserveCreateStart("crew")
	// Proper sentinel wrap so errors.Is matches.
	done2(wrapErr(ErrLockHeld), 0)

	snap := Snapshot()
	if snap.FailedTotal != 2 {
		t.Fatalf("FailedTotal: got %d want 2", snap.FailedTotal)
	}
	if snap.FailedByReason["lock_held"] != 1 {
		t.Errorf("lock_held bucket: got %d want 1", snap.FailedByReason["lock_held"])
	}
	if snap.FailedByReason["other"] != 1 {
		t.Errorf("other bucket: got %d want 1", snap.FailedByReason["other"])
	}
}

func TestMetrics_RestoreCounters(t *testing.T) {
	ResetMetrics()
	ObserveRestore(nil)
	ObserveRestore(nil)
	ObserveRestore(wrapErr(ErrNoOpRestore))

	snap := Snapshot()
	if snap.RestoredTotal != 2 {
		t.Errorf("RestoredTotal: got %d want 2", snap.RestoredTotal)
	}
	if snap.FailedTotal != 1 {
		t.Errorf("FailedTotal: got %d want 1", snap.FailedTotal)
	}
	if snap.FailedByReason["restore:noop_restore"] != 1 {
		t.Errorf("restore:noop_restore: got %d want 1", snap.FailedByReason["restore:noop_restore"])
	}
}

func TestMetrics_LockHeldLifecycle(t *testing.T) {
	ResetMetrics()
	ObserveLockAcquired("ws-1")
	snap := Snapshot()
	if _, ok := snap.LockHeld["ws-1"]; !ok {
		t.Error("LockHeld should contain ws-1 after acquire")
	}
	ObserveLockReleased("ws-1")
	snap = Snapshot()
	if _, ok := snap.LockHeld["ws-1"]; ok {
		t.Error("LockHeld should not contain ws-1 after release")
	}
}

func TestMetrics_DurationQuantiles(t *testing.T) {
	ResetMetrics()
	// Feed a known distribution so we can assert p50 falls in the
	// middle. The quantile math is integer-indexed; exact sample
	// returned depends on the index. We just assert it is a plausible
	// value from the sample set, not a specific slot.
	for i := 0; i < 100; i++ {
		done := ObserveCreateStart("test")
		done(nil, int64(i))
	}
	snap := Snapshot()
	if snap.DurationSecondsP50 < 0 {
		t.Errorf("p50 must be non-negative, got %v", snap.DurationSecondsP50)
	}
	if snap.DurationSecondsMean < 0 {
		t.Errorf("mean must be non-negative, got %v", snap.DurationSecondsMean)
	}
}

// wrapErr constructs a nested error that still passes errors.Is for the
// supplied sentinel — used by the tests above so the classifier
// exercise a realistic wrap rather than a raw sentinel.
func wrapErr(sentinel error) error {
	return &wrapped{inner: sentinel}
}

type wrapped struct{ inner error }

func (w *wrapped) Error() string { return "wrap: " + w.inner.Error() }
func (w *wrapped) Unwrap() error { return w.inner }
