package pipeline

import (
	"context"
	"testing"
	"time"
)

// #1428 (2.9) — a boot/approval-resumed run parked on a still-pending
// approval must RE-PARK (release its slot) rather than block on WaitFor for
// up to the 24h approval timeout. Otherwise every resumed-but-not-yet-decided
// approval permanently occupies a concurrency slot.
func TestRun_ResumeParkedApproval_HoldsNoSlot(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	registry := NewRunRegistry()
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-repark", asyncApprovalLinearDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
		WithRunStore(runStore).
		WithRunRegistry(registry).
		WithWaitpointStore(wpStore)

	// Initial run parks on the approval and releases its slot.
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("expected WAITING, got %q", res.Status)
	}
	if registry.IsLive(res.RunID) {
		t.Fatalf("initial park should not hold a slot")
	}

	// Drive the resume synchronously (mirrors runResumedRun) so the test is
	// deterministic: with the fix Run RETURNS WAITING (re-parked); without it
	// Run BLOCKS on WaitFor holding the re-acquired slot.
	rec, gerr := runStore.Get(ctx, res.RunID)
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	resumeIn := RunInput{
		PipelineID:           rec.PipelineID,
		WorkspaceID:          rec.WorkspaceID,
		Mode:                 ModeRun,
		RunIDOverride:        rec.ID,
		resume:               true,
		resumeReason:         resumeReasonApproval,
		restoredOutputs:      map[string]string{}, // wait step didn't complete, nothing restored
		resumeDefinitionHash: rec.DefinitionHash,
		resumeCurrentStepID:  rec.CurrentStepID,
	}
	done := make(chan *RunResult, 1)
	go func() {
		r, rerr := exec.Run(ctx, resumeIn)
		if rerr != nil {
			t.Errorf("resume run: %v", rerr)
		}
		done <- r
	}()

	select {
	case r := <-done:
		if r.Status != "WAITING" {
			t.Errorf("resumed run status = %q, want WAITING (re-parked)", r.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resumed run blocked on WaitFor instead of re-parking — it holds a concurrency slot")
	}

	// Slot released and the run is still parked.
	if registry.IsLive(res.RunID) {
		t.Error("re-parked run still holds a concurrency slot")
	}
	rec2, _ := runStore.Get(ctx, res.RunID)
	if rec2.Status != RunStatusWaiting {
		t.Errorf("re-parked run status = %q, want waiting", rec2.Status)
	}
}
