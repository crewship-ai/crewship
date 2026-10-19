package pipeline

import (
	"testing"
	"time"
)

// This is a storage-level lost-owner reproduction, not a kill -9 end-to-end test.
// No executor or provider is invoked between ClaimDue and opening a fresh store.
func TestReviewPendingPreStartClaimRemainsRecoverable(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	before := NewPendingRunStore(db)
	now := time.Now().UTC()
	_, _, err := before.Enqueue(t.Context(), PendingRun{ID: "review-no-start", WorkspaceID: "review-w", PipelineID: "review-p", PipelineSlug: "review", FireAt: now.Add(-time.Minute), TriggeredVia: TriggeredViaWebhook})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := before.ClaimDue(t.Context(), "review-no-start", now)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	// Simulate losing the caller before Executor.Run. SQL bytes survive.
	after := NewPendingRunStore(db)
	due, err := after.DueRuns(t.Context(), now.Add(time.Hour), 25)
	if err != nil {
		t.Fatal(err)
	}
	row, err := after.Get(t.Context(), "review-w", "review-no-start")
	if err != nil || row == nil {
		t.Fatalf("receipt missing: %+v %v", row, err)
	}
	if len(due) == 0 && row.FiredRunID == "" && row.Status == "fired" {
		t.Fatalf("accepted claim is stranded: status=%s fired_run_id=%q due=%d; no executor was invoked", row.Status, row.FiredRunID, len(due))
	}
}

func TestReviewPendingWebhookAttributionRoundTrip(t *testing.T) {
	db := newPendingDB(t)
	defer db.Close()
	s := NewPendingRunStore(db)
	now := time.Now().UTC()
	_, _, err := s.Enqueue(t.Context(), PendingRun{ID: "review-origin", WorkspaceID: "review-w", PipelineID: "review-p", PipelineSlug: "review", FireAt: now.Add(-time.Minute), TriggeredVia: TriggeredViaWebhook, TriggeredByID: "review-webhook"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.ClaimDue(t.Context(), "review-origin", now)
	if err != nil || row == nil {
		t.Fatalf("claim: %+v %v", row, err)
	}
	via, by := effectivePendingTrigger(*row)
	if via != TriggeredViaWebhook || by != "review-webhook" {
		t.Fatalf("origin changed: %s/%s", via, by)
	}
}
