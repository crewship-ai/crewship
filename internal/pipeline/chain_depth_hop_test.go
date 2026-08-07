package pipeline

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The composition budget across the journal hop.
//
// This file was written during an adversarial pass, when the budget did NOT
// survive the hop: pending_runs had no depth column, PendingRunDispatcher
// built its RunInput without one, and an automation-fired run therefore opened
// a fresh allowance every lap. A two-rule cycle ran 59 hops in five minutes
// past a cap of 8. Both tests here were skipped as BROKEN, describing a fix
// that had not been written.
//
// The depth half landed: pending_runs.chain_depth is carried, Registry.Flush
// prices the hop (in Flush and never in Observer, which is on the journal write
// path), and internal/automation's TestObserver_ClosedLoopStopsAtMaxChainDepth
// holds a closed loop to MaxChainDepth with MaxPerHour raised to 10000 so the
// rate limiter cannot be what makes it pass.
//
// What did NOT land with it is the ORIGIN. A run's chain_origin names the run
// or entry that started the chain, and it is what makes eight composed hops
// read as one chain instead of eight unrelated runs. pending_runs carried no
// origin, so every automation-fired run re-rooted at itself — the budget was
// spent correctly and the story of spending it was lost. The depth cap is a
// safety property; the origin is the legibility property, and this epic is
// about both.
// ---------------------------------------------------------------------------

// An automation-fired run must inherit the chain budget AND the chain origin of
// whatever produced the entry it matched. Written against the seam the fix
// lands on: the RunInput the dispatcher hands the executor.
func TestChainDepth_SurvivesTheAutomationHop(t *testing.T) {
	ctx := context.Background()
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	past := time.Now().Add(-time.Minute)

	// A rule-fired run seven hops deep, rooted at prn_root. Registry.Flush has
	// already priced this hop — the row records the depth this run IS at, so
	// the dispatcher passes it through rather than incrementing it a second
	// time. Two places adding one is how a cap ends up half its stated size.
	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_deep", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past,
		TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
		ChainDepth:  7,
		ChainOrigin: "prn_root",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	d.Start(ctx)
	defer d.Stop()

	if !waitFor(t, barrierAbandonAfter, func() bool {
		exec.seenMu.Lock()
		defer exec.seenMu.Unlock()
		return len(exec.seen) >= 1
	}) {
		t.Fatal("dispatcher never fired the row")
	}

	exec.seenMu.Lock()
	defer exec.seenMu.Unlock()
	if got := exec.seen[0].ChainDepth; got != 7 {
		t.Fatalf("ChainDepth = %d, want 7 — an automation-fired run must spend from the "+
			"chain budget of whatever caused the entry it matched, not open a new one", got)
	}
	if got := exec.seen[0].ChainOrigin; got != "prn_root" {
		t.Fatalf("ChainOrigin = %q, want %q — a re-rooted chain reads as several short "+
			"chains, which is exactly what a loop would like to look like", got, "prn_root")
	}
}

// A deferred run with no composed parent is the start of its own chain, not a
// hop in somebody else's. Zero and empty must round-trip as zero and empty:
// inventing an origin here would make every scheduled run look composed.
func TestChainDepth_AScheduledRunStartsItsOwnChain(t *testing.T) {
	ctx := context.Background()
	db := newPendingDB(t)
	s := NewPendingRunStore(db)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_plain", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		FireAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	exec := &fakeExecutor{}
	d := NewPendingRunDispatcher(s, exec, nil)
	d.Start(ctx)
	defer d.Stop()

	if !waitFor(t, barrierAbandonAfter, func() bool {
		exec.seenMu.Lock()
		defer exec.seenMu.Unlock()
		return len(exec.seen) >= 1
	}) {
		t.Fatal("dispatcher never fired the row")
	}

	exec.seenMu.Lock()
	defer exec.seenMu.Unlock()
	if got := exec.seen[0].ChainDepth; got != 0 {
		t.Errorf("ChainDepth = %d, want 0", got)
	}
	if got := exec.seen[0].ChainOrigin; got != "" {
		t.Errorf("ChainOrigin = %q, want empty — the executor roots the chain at this run; "+
			"a value supplied here would claim a parent that does not exist", got)
	}
}
