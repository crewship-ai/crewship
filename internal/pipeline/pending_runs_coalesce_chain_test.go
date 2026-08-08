package pipeline

import (
	"context"
	"testing"
	"time"
)

// Coalescing must not hand back composition budget.
//
// coalesceDebounce adopts the later trigger's payload, invoking user and
// byline — and did not adopt its chain position. A deeper hop merging into a
// shallower pending row therefore fired at the SHALLOWER depth with the
// shallower origin, which re-roots the chain and re-opens the budget. Two ways
// in, both reachable:
//
//   - a user's deferred run with debounce_key "nightly" sits at depth 0; an
//     automation at depth 6 coalesces into it, and the fired run claims the
//     rule's byline (which the store deliberately moves) while spending from
//     depth 0 — the next eight hops are granted afresh;
//   - inside a live cycle, a depth-N+2 hop coalescing into a still-pending
//     depth-N row under the same auto:<id>:mission:<m> key under-charges by 2.
//
// Found by scripts/test-harness/test-automation-loop.sh, which produced ten
// status changes against a cap of nine and reported two distinct chain
// origins. The cap held everywhere else; this is the one door that gave change
// back.
//
// The rule is the DEEPER position wins, not the later one. Attribution follows
// the payload because a forged byline is the harm there; the budget follows
// the deepest claimant because under-charging is the harm here. A coalesced
// row can only ever be charged too much, never too little.
func TestCoalesceDebounce_TakesTheDeeperChainPosition(t *testing.T) {
	ctx := context.Background()
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	past := time.Now().Add(-time.Minute)

	shallow := PendingRun{
		ID: "p_shallow", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k", FireAt: past,
	}
	if _, _, err := s.Enqueue(ctx, shallow); err != nil {
		t.Fatalf("enqueue shallow: %v", err)
	}

	deep := PendingRun{
		ID: "p_deep", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k", FireAt: past,
		TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
		ChainDepth: 6, ChainOrigin: "prn_root",
	}
	if _, _, err := s.Enqueue(ctx, deep); err != nil {
		t.Fatalf("enqueue deep (coalesces): %v", err)
	}

	rows, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 — the two enqueues must have coalesced", len(rows))
	}
	got := rows[0]

	if got.ChainDepth != 6 {
		t.Errorf("ChainDepth = %d, want 6 — a deeper hop coalescing into a shallower row must "+
			"not spend from the shallower budget; that is how a cycle buys itself another eight hops",
			got.ChainDepth)
	}
	if got.ChainOrigin != "prn_root" {
		t.Errorf("ChainOrigin = %q, want %q — dropping it re-roots the chain, and a bounded "+
			"cycle then reads as several short unrelated ones", got.ChainOrigin, "prn_root")
	}
}

// The other order. A shallow trigger arriving second must not DROP the depth
// the row already carries: the run is caused by both, and the deepest claimant
// is the one the budget has to answer to.
func TestCoalesceDebounce_AShallowerLaterTriggerDoesNotResetTheBudget(t *testing.T) {
	ctx := context.Background()
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_deep", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k", FireAt: past,
		ChainDepth: 6, ChainOrigin: "prn_root",
	}); err != nil {
		t.Fatalf("enqueue deep: %v", err)
	}
	// A human deferring the same routine under the same key, at depth 0.
	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_human", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "k", FireAt: past, InvokingUserID: "usr_1",
	}); err != nil {
		t.Fatalf("enqueue human (coalesces): %v", err)
	}

	rows, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := rows[0].ChainDepth; got != 6 {
		t.Errorf("ChainDepth = %d, want 6 — a shallow trigger arriving second must not hand "+
			"the composed chain its budget back", got)
	}
	if got := rows[0].ChainOrigin; got != "prn_root" {
		t.Errorf("ChainOrigin = %q, want %q", got, "prn_root")
	}
	// Attribution still follows the payload — that rule is unchanged, and this
	// pins that the fix did not quietly reverse it.
	if got := rows[0].InvokingUserID; got != "usr_1" {
		t.Errorf("InvokingUserID = %q, want usr_1 — the later trigger's payload fires, so its "+
			"user is who a `to: trigger` notify must reach", got)
	}
}
