package orchestrator

import (
	"testing"
	"time"
)

// TestLockSidecarLifecycle_SameContainerExcludes: a second acquisition for the
// SAME container must block until the first is released.
func TestLockSidecarLifecycle_SameContainerExcludes(t *testing.T) {
	o := &Orchestrator{} // zero-value sync.Map must be usable

	unlock := o.lockSidecarLifecycle("c1")

	acquired := make(chan struct{})
	go func() {
		u := o.lockSidecarLifecycle("c1")
		close(acquired)
		u()
	}()

	select {
	case <-acquired:
		t.Fatal("second lock for the same container acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}

	unlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after the first was released")
	}
}

// TestLockSidecarLifecycle_DifferentContainersIndependent: locks for different
// containers must not exclude each other (no global lock).
func TestLockSidecarLifecycle_DifferentContainersIndependent(t *testing.T) {
	o := &Orchestrator{}

	unlockA := o.lockSidecarLifecycle("c-a")
	defer unlockA()

	acquired := make(chan struct{})
	go func() {
		u := o.lockSidecarLifecycle("c-b")
		close(acquired)
		u()
	}()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("lock for a DIFFERENT container blocked behind an unrelated one — #1220 lock must be per-container")
	}
}

// TestLockSidecarLifecycle_UnlockIdempotent: the returned unlock must tolerate
// being called twice (explicit happy-path release + deferred error-path cover).
func TestLockSidecarLifecycle_UnlockIdempotent(t *testing.T) {
	o := &Orchestrator{}

	unlock := o.lockSidecarLifecycle("c1")
	unlock()
	unlock() // must not panic ("unlock of unlocked mutex") or corrupt state

	// The lock must be re-acquirable afterwards.
	done := make(chan struct{})
	go func() {
		u := o.lockSidecarLifecycle("c1")
		u()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("lock not re-acquirable after idempotent double unlock")
	}
}
