package pipeline

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Attribution across a coalesce
//
// coalesceDebounce adopts the LATEST trigger's inputs, tags, metadata, tier,
// priority and invoking user, and says why: "Coalescing adopts the LATEST
// trigger's payload, so it must also adopt its invoking user — attribution
// follows the payload it belongs to."
//
// triggered_via / triggered_by_id were added later and were not added to that
// UPDATE, so the one field that is literally named "attribution" is the one it
// does not follow. The debounce key is caller-supplied on
// POST /api/v1/pipelines/{slug}/defer, so the two producers can meet on one
// row in either order, and whoever got there FIRST keeps the byline.
// ---------------------------------------------------------------------------

// A user's deferred run lands on a row an automation created. The run fires
// with the user's inputs — and must not fire under the automation's name.
func TestPendingRuns_CoalesceAdoptsTheLatestTrigger(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_auto", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "auto:aut_abc:mission:m_1", InputsJSON: `{"who":"automation"}`,
		FireAt:        past,
		TriggeredVia:  TriggeredViaAutomation,
		TriggeredByID: "aut_abc",
	}); err != nil {
		t.Fatalf("enqueue automation: %v", err)
	}

	// The same debounce key, from the deferred-run endpoint. Nothing stops a
	// caller naming it: debounce_key is free-form request data.
	id, coalesced, err := s.Enqueue(ctx, PendingRun{
		ID: "p_user", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "auto:aut_abc:mission:m_1", InputsJSON: `{"who":"user"}`,
		FireAt:         past,
		InvokingUserID: "usr_1",
	})
	if err != nil {
		t.Fatalf("enqueue user: %v", err)
	}
	if !coalesced || id != "p_auto" {
		t.Fatalf("coalesced=%v id=%q, want true/p_auto", coalesced, id)
	}

	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due rows = %d, want 1", len(due))
	}
	if due[0].InputsJSON != `{"who":"user"}` {
		t.Fatalf("inputs = %s, want the later trigger's", due[0].InputsJSON)
	}
	via, by := effectivePendingTrigger(due[0])
	if via == TriggeredViaAutomation || by == "aut_abc" {
		t.Fatalf("the run fires with the user's inputs but claims triggered_via=%q "+
			"triggered_by_id=%q — coalescing kept the FIRST producer's byline while "+
			"adopting the SECOND's payload, so the run record names a rule that did not "+
			"cause it", via, by)
	}
	if via != TriggeredViaSchedule {
		t.Fatalf("triggered_via = %q, want the dispatcher's documented default for an "+
			"unattributed producer (%q)", via, TriggeredViaSchedule)
	}
}

// The mirror image, which is the one that loses an audit trail rather than
// forging one: an automation coalescing into a row a user's defer created must
// hand the run the automation's byline, not leave it reading as a cron.
func TestPendingRuns_CoalesceLetsAnAutomationClaimTheRow(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_user", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "nightly", InputsJSON: `{"who":"user"}`, FireAt: past,
	}); err != nil {
		t.Fatalf("enqueue user: %v", err)
	}
	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_auto", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		DebounceKey: "nightly", InputsJSON: `{"who":"automation"}`, FireAt: past,
		TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
	}); err != nil {
		t.Fatalf("enqueue automation: %v", err)
	}

	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("due rows = %d, want 1", len(due))
	}
	via, by := effectivePendingTrigger(due[0])
	if via != TriggeredViaAutomation || by != "aut_abc" {
		t.Fatalf("triggered_via/%s by/%s, want automation/aut_abc — the automation's "+
			"payload won the row but its byline did not, so the rule-fired run reports a cron",
			via, by)
	}
}
