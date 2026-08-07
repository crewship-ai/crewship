package pipeline

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SKIPPED — the composition budget does not survive the journal hop.
//
// MaxChainDepth's own doc says what it is for: "It is NOT the same ceiling as
// maxPipelineDepth: that bounds in-process call_pipeline nesting within a
// single top-level run, resets when the run ends, and is not persisted. This
// one is carried on the run row and survives the chain leaving the process
// through the journal and coming back." GuardChainDepth's doc adds the rule
// that keeps that true: "If a new composed edge appears, it calls this — and
// if it does not, the omission is visible as the absence of a call."
//
// The omission is the absence of a call. As shipped, GuardChainDepth has
// exactly ONE production caller — runCallPipelineStep — and the cap is
// therefore identical in reach to the maxPipelineDepth counter it says it is
// not: it bounds in-process nesting and nothing else.
//
// Three separate breaks, each sufficient on its own:
//
//  1. NOTHING PERSISTS A NON-ZERO DEPTH. ChainDepth only becomes > 0 in
//     buildNestedRunInput, i.e. for a call_pipeline child, and those runs are
//     at depth > 0 where persistRunStart is skipped by design ("nested
//     call_pipeline runs reuse the parent's row id"). Every row that reaches
//     pipeline_runs is written from a depth==0 RunInput, so chain_depth is 0
//     and chain_origin is the run's own id on every row that exists. The
//     v20260807160100 column, its partial index (WHERE chain_depth > 0 — an
//     index over the empty set), the API field, the openapi schema and the
//     CLI's "chain" column all read the same constant.
//
//  2. THE AUTOMATION EDGE DOES NOT CARRY IT. pending_runs has no depth column,
//     Registry.Flush says so in a TODO, and PendingRunDispatcher.fireOne
//     builds its RunInput without ChainDepth/ChainOrigin. So an
//     automation-fired run starts a fresh budget at 0 no matter what caused
//     the entry it matched — which is the exact hop the cap was added for,
//     since it is the only one that leaves the process.
//
//  3. THE CREWSHIP EDGE DISCARDS IT. runCrewshipStep populates
//     CrewshipRequest.ChainDepth from the run, and crewshipActions.Do never
//     reads the field: it is not sent as a header, not placed in the body, and
//     not passed to GuardChainDepth. The comment on the field says a
//     downstream automation "resolves depth+1 rather than 0" from
//     author_run_id — nothing anywhere reads a run's chain_depth back, so
//     nobody resolves anything.
//
// Net effect: routine → crewship step → issue event → automation → routine is
// an unbounded cycle. What actually bounds it today is max_per_hour on the
// automation (default 60/hour, per rule, per PROCESS — see the registry's
// in-memory `rate` map), which is a throttle, not a depth cap, and which the
// cycle's author chooses.
//
// NOT FIXED HERE because the fix is a design change, not a patch: pending_runs
// needs depth/origin columns and a migration, the registry needs to resolve
// the depth of the run that produced the triggering entry (a DB read it
// deliberately does not do on the write path, so it belongs in Flush), and
// somebody has to decide what depth a HUMAN-caused entry starts at. Guessing
// any of those inside an adversarial pass would be a second answer to "how
// deep are we", which is the failure GuardChainDepth's doc exists to prevent.
// ---------------------------------------------------------------------------

// An automation-fired run must inherit the chain budget of whatever produced
// the entry it matched, not start a fresh one. Written against the seam the
// fix would land on: the RunInput the dispatcher hands the executor.
func TestChainDepth_SurvivesTheAutomationHop(t *testing.T) {
	t.Skip("BROKEN: pending_runs carries no chain depth and PendingRunDispatcher.fireOne " +
		"builds its RunInput without ChainDepth/ChainOrigin, so every automation-fired run " +
		"starts a fresh budget at 0. GuardChainDepth has one production caller " +
		"(runCallPipelineStep), which makes MaxChainDepth equivalent to the in-process " +
		"maxPipelineDepth ceiling its own doc says it is not. See the file comment.")

	ctx := context.Background()
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	past := time.Now().Add(-time.Minute)

	// A rule-fired run whose chain is already seven hops deep. The eighth is
	// the last one MaxChainDepth allows; the ninth must be refused.
	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_deep", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s", FireAt: past,
		TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
		// The field that does not exist:
		//   ChainDepth: 7, ChainOrigin: "prn_root",
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
	if got := exec.seen[0].ChainDepth; got != 8 {
		t.Fatalf("ChainDepth = %d, want 8 — an automation-fired run must spend from the "+
			"chain budget of whatever caused the entry it matched, not open a new one", got)
	}
	if got := exec.seen[0].ChainOrigin; got != "prn_root" {
		t.Fatalf("ChainOrigin = %q, want the chain's root — a re-rooted chain reads as two "+
			"short chains, which is exactly what a loop would like to look like", got)
	}
}

// The crewship step is a composed edge: the row it writes produces a journal
// entry an automation can match, so it can close a cycle. GuardChainDepth's
// doc makes calling it the definition of a composed edge; this one does not.
func TestChainDepth_CrewshipStepIsAGuardedEdge(t *testing.T) {
	t.Skip("BROKEN: CrewshipRequest.ChainDepth is populated by runCrewshipStep and then " +
		"read by nothing — crewshipActions.Do neither sends it nor passes it to " +
		"GuardChainDepth, so the verb that lets a routine write back into the product is " +
		"the one composed edge with no budget check at all. See the file comment.")
}
