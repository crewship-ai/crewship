package docker

import (
	"sync"
	"testing"
	"time"
)

// TestLockForCrew_SameCrewSerializes proves the per-crew mutex prevents
// the "Conflict: container name already in use" race that bit us when
// dispatching N issues to the same crew in the same instant. Two
// concurrent callers for the same crew_id must see each other.
func TestLockForCrew_SameCrewSerializes(t *testing.T) {
	p := &Provider{}

	mu1 := p.lockForCrew("crew-A")
	mu2 := p.lockForCrew("crew-A")

	if mu1 != mu2 {
		t.Fatal("two lockForCrew calls for the same crew_id must return the same mutex")
	}
}

// Different crews → different mutexes, so unrelated crews still
// dispatch in parallel.
func TestLockForCrew_DifferentCrewsParallel(t *testing.T) {
	p := &Provider{}

	a := p.lockForCrew("crew-A")
	b := p.lockForCrew("crew-B")

	if a == b {
		t.Fatal("different crew_ids must get distinct mutexes")
	}
}

// Concurrent first-time access to the same crew_id from many goroutines
// must still return one shared mutex (LoadOrStore vs raw Store
// regression check).
func TestLockForCrew_ConcurrentInit(t *testing.T) {
	p := &Provider{}

	const N = 100
	var wg sync.WaitGroup
	results := make([]*sync.Mutex, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i] = p.lockForCrew("crew-X")
		}()
	}
	wg.Wait()

	for i := 1; i < N; i++ {
		if results[0] != results[i] {
			t.Fatalf("goroutine %d got a different mutex from goroutine 0 — LoadOrStore was bypassed", i)
		}
	}
}

// Verifies that holding the lock for one crew does NOT block calls for
// another crew — different crew dispatches must remain parallel even
// when one crew is mid-create.
func TestLockForCrew_OneCrewLockedOthersFree(t *testing.T) {
	p := &Provider{}
	muA := p.lockForCrew("crew-A")
	muA.Lock()
	defer muA.Unlock()

	done := make(chan struct{})
	go func() {
		muB := p.lockForCrew("crew-B")
		// Acquiring muB IS the assertion — if it were the same mutex as
		// crew-A's, Lock would block forever and the select below would
		// hit the timeout.
		muB.Lock()
		defer muB.Unlock()
		close(done)
	}()

	select {
	case <-done:
		// good — other crews are unaffected
	case <-time.After(2 * time.Second):
		t.Fatal("crew-B mutex was blocked by crew-A's lock — they should be independent")
	}
}
