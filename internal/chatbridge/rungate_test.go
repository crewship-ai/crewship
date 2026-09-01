package chatbridge

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunGate_ExcludesConcurrentClaims is the core exclusivity proof for
// F51: two callers racing to claim the SAME key must never both believe
// they hold it. On main (before RunGate existed), the only exclusivity
// primitive was Bridge.tryMarkRunStart keyed by chat id — the assignment
// path (api.AssignmentHandler.runAssignment) never called it at all, so
// this guarantee did not exist for it. This test targets the extracted
// primitive directly; TestRunAssignment_ConcurrentSameAgent_SecondIsRefused
// (internal/api) proves the same thing at the runAssignment call site.
//
// Run with -race: the shared `holders` counter is only safe because the
// mutex inside RunGate really does serialise TryStart/End: a broken
// implementation (e.g. a plain map with no lock) would race here.
func TestRunGate_ExcludesConcurrentClaims(t *testing.T) {
	t.Parallel()
	g := NewRunGate()
	const key = "agent-shared"
	const workers = 16

	var holders int32
	var maxHolders int32
	var wg sync.WaitGroup
	var successes int32

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !g.TryStart(key) {
				return
			}
			atomic.AddInt32(&successes, 1)
			// Hold the claim briefly so overlapping TryStart calls from
			// other goroutines land while this one is still active —
			// without this window, workers could serialise so fast that
			// two never overlap even with a broken (unlocked) gate.
			n := atomic.AddInt32(&holders, 1)
			for {
				old := atomic.LoadInt32(&maxHolders)
				if n <= old || atomic.CompareAndSwapInt32(&maxHolders, old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&holders, -1)
			g.End(key)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxHolders); got != 1 {
		t.Errorf("max concurrent holders of key %q = %d, want 1 (exclusivity violated)", key, got)
	}
	if got := atomic.LoadInt32(&successes); got == 0 {
		t.Fatal("no worker ever claimed the key — test is broken, not the gate")
	}
	if g.InFlight(key) {
		t.Error("key still reports in-flight after every worker released its claim")
	}
}

// TestRunGate_IndependentKeysDoNotBlockEachOther guards against an
// over-eager fix that serialises everything behind one lock: two
// DIFFERENT agents must be able to run at the same time.
func TestRunGate_IndependentKeysDoNotBlockEachOther(t *testing.T) {
	t.Parallel()
	g := NewRunGate()
	if !g.TryStart("agent-a") {
		t.Fatal("expected first claim on agent-a to succeed")
	}
	if !g.TryStart("agent-b") {
		t.Error("claim on agent-b must not be blocked by an unrelated agent-a claim")
	}
	g.End("agent-a")
	g.End("agent-b")
}

// TestRunGate_ReentrantClaimFails documents the exclusivity contract on a
// single goroutine: TryStart must fail while its own prior claim on the
// same key is still held, exactly like tryMarkRunStart.
func TestRunGate_ReentrantClaimFails(t *testing.T) {
	t.Parallel()
	g := NewRunGate()
	if !g.TryStart("agent-x") {
		t.Fatal("first claim should succeed")
	}
	if g.TryStart("agent-x") {
		t.Error("second claim on the same still-held key must fail")
	}
	g.End("agent-x")
	if !g.TryStart("agent-x") {
		t.Error("claim should succeed again once released")
	}
	g.End("agent-x")
}
