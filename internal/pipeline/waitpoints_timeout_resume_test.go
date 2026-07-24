package pipeline

import (
	"context"
	"testing"
	"time"
)

// #1425 (unit) — the 30s sweeper flips an expired waitpoint to timed_out but
// historically never resumed the parked run, so the run stayed 'waiting'
// until a restart. The sweeper must now invoke the wired resume hook with the
// waitpoint's pipeline_run_id.
func TestSweepOnce_TriggersResumer(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	got := make(chan string, 1)
	store.SetTimeoutResumer(func(runID string) { got <- runID })

	// seedPendingWaitpoint inserts pipeline_run_id='run_1'.
	seedPendingWaitpoint(t, store, "tok-resume", time.Now().Add(-1*time.Hour))
	store.sweepOnce()

	select {
	case runID := <-got:
		if runID != "run_1" {
			t.Errorf("resumer called with runID=%q, want run_1", runID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sweepOnce did not invoke the timeout resumer for the expired waitpoint")
	}
}

// A row already terminal (RowsAffected==0) must NOT trigger a resume — only a
// waitpoint THIS sweep flipped should cascade.
func TestSweepOnce_NoResumeForAlreadyTerminal(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	fired := make(chan string, 1)
	store.SetTimeoutResumer(func(runID string) { fired <- runID })

	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO pipeline_waitpoints
(token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES ('tok-done', 'ws_test', 'run_done', 'gate', 'approval', 'approved', ?)`,
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.sweepOnce()

	select {
	case runID := <-fired:
		t.Fatalf("resumer fired for an already-terminal waitpoint (runID=%q)", runID)
	case <-time.After(300 * time.Millisecond):
		// expected: no resume
	}
}

// #1425 (acceptance) — a run parked on an approval waitpoint whose timeout
// elapses must reach a terminal state WITHOUT a restart. The live sweeper,
// wired to the executor's resume path, drives the parked run to FAILED
// (the wait step reports the timeout) in-process.
func TestWaitpointTimeout_ResumesParkedRun_NoRestart(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-timeout", asyncApprovalLinearDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	// Wire the sweeper -> executor resume path (mirrors the boot wiring).
	wpStore.SetTimeoutResumer(func(runID string) { exec.ResumeAfterApproval(runID, nil) })

	// Park the run on the approval.
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("expected WAITING, got %q", res.Status)
	}
	rec, _ := runStore.Get(ctx, res.RunID)
	if rec.Status != RunStatusWaiting {
		t.Fatalf("run row not parked: %q", rec.Status)
	}

	// Elapse the waitpoint's timeout, then fire the live sweep (no restart).
	if _, err := db.ExecContext(ctx,
		`UPDATE pipeline_waitpoints SET timeout_at = ? WHERE token = ?`,
		time.Now().Add(-1*time.Hour).UTC().Format(time.RFC3339Nano), res.WaitpointToken); err != nil {
		t.Fatalf("elapse timeout: %v", err)
	}
	wpStore.sweepOnce()

	// The parked run must reach a terminal state on its own.
	final := waitForRunStatus(t, runStore, res.RunID, RunStatusFailed, 5*time.Second)
	if final.Status != RunStatusFailed {
		t.Fatalf("expected FAILED after timeout resume, got %q", final.Status)
	}
}
