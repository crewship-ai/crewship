package pipeline

import (
	"context"
	"testing"
	"time"
)

// A deferred run used to arrive claiming a schedule fired it, whoever
// actually did.
//
// PendingRunDispatcher hard-coded `TriggeredVia: TriggeredViaSchedule` and
// `TriggeredByID: pr.ID` for every row, because pending_runs carried no
// attribution to honour. That was survivable while the only producer was
// the deferred-run endpoint. It stopped being survivable when automations
// started enqueueing here: every automation-fired run told the operator a
// cron did it, and the rule's identity survived only as a shape inside
// metadata_json that a reader had to reverse-engineer.
//
// The provenance belongs on the row.
func TestPendingRuns_AttributionRoundTrip(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_auto", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		FireAt:        past,
		TriggeredVia:  TriggeredViaAutomation,
		TriggeredByID: "aut_abc",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due runs, want 1", len(due))
	}
	if due[0].TriggeredVia != TriggeredViaAutomation {
		t.Errorf("TriggeredVia = %q, want %q — a rule must not read as a cron",
			due[0].TriggeredVia, TriggeredViaAutomation)
	}
	if due[0].TriggeredByID != "aut_abc" {
		t.Errorf("TriggeredByID = %q, want the automation id", due[0].TriggeredByID)
	}
}

// Rows written before the columns existed, and every producer that does not
// care, must keep behaving exactly as they did. A migration that changes
// what old rows mean is a migration that rewrites history.
func TestPendingRuns_UnattributedStillFiresAsASchedule(t *testing.T) {
	db := newPendingDB(t)
	s := NewPendingRunStore(db)
	ctx := context.Background()

	if _, _, err := s.Enqueue(ctx, PendingRun{
		ID: "p_plain", WorkspaceID: "w", PipelineID: "pl", PipelineSlug: "s",
		FireAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	due, err := s.DueRuns(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due runs, want 1", len(due))
	}
	via, by := effectivePendingTrigger(due[0])
	if via != TriggeredViaSchedule {
		t.Fatalf("an unattributed row must still fire as %q, got %q", TriggeredViaSchedule, via)
	}
	if by != "p_plain" {
		t.Fatalf("an unattributed row must still point at its own pending id, got %q", by)
	}
}

// The attributed row must NOT be rewritten to point at itself — that is the
// whole bug.
func TestPendingRuns_AttributedRowKeepsItsOwnTrigger(t *testing.T) {
	via, by := effectivePendingTrigger(PendingRun{
		ID: "p_auto", TriggeredVia: TriggeredViaAutomation, TriggeredByID: "aut_abc",
	})
	if via != TriggeredViaAutomation || by != "aut_abc" {
		t.Fatalf("effectivePendingTrigger rewrote an attributed row to %q/%q", via, by)
	}
}
