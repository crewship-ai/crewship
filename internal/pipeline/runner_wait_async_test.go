package pipeline

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// Async WAITING model: a top-level foreground run that hits a wait(approval)
// step must PARK (return WAITING promptly with a pollable waitpoint) instead of
// blocking in WaitFor, and approving must resume it to COMPLETED.

const asyncApprovalLinearDSL = `{
  "dsl_version": "1.0",
  "name": "appr-linear",
  "steps": [
    {"id": "gate", "type": "wait", "wait": {"kind": "approval", "approval_prompt": "ship it?"}},
    {"id": "done", "type": "transform", "transform": {"input": "shipped", "expression": "."}}
  ]
}`

// TestRun_ApprovalWaitStep_ReturnsWaiting — linear path. First step is a
// wait(approval): Run returns promptly WAITING + token; the waitpoint is
// queryable; CompleteApproval + resume drives the run to COMPLETED.
func TestRun_ApprovalWaitStep_ReturnsWaiting(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-linear", asyncApprovalLinearDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	// Run must return PROMPTLY (not block on the approval).
	done := make(chan *RunResult, 1)
	go func() {
		res, err := exec.Run(ctx, RunInput{
			PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()

	var res *RunResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run blocked on the approval instead of returning WAITING")
	}

	if res.Status != "WAITING" {
		t.Fatalf("status: got %q, want WAITING", res.Status)
	}
	if res.WaitpointToken == "" {
		t.Fatal("WAITING result missing waitpoint token")
	}
	if res.CurrentStep != "gate" {
		t.Errorf("current step: got %q, want gate", res.CurrentStep)
	}

	// The run row is parked, not terminal.
	rec, err := runStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != RunStatusWaiting {
		t.Fatalf("run row status: got %q, want waiting", rec.Status)
	}

	// The waitpoint is pollable: pending, right run + step.
	var wpRun, wpStep, wpStatus string
	if err := db.QueryRowContext(ctx,
		`SELECT pipeline_run_id, step_id, status FROM pipeline_waitpoints WHERE token = ?`,
		res.WaitpointToken).Scan(&wpRun, &wpStep, &wpStatus); err != nil {
		t.Fatalf("waitpoint query: %v", err)
	}
	if wpStatus != "pending" || wpRun != res.RunID || wpStep != "gate" {
		t.Errorf("waitpoint row: run=%q step=%q status=%q, want run=%q step=gate status=pending", wpRun, wpStep, wpStatus, res.RunID)
	}

	// Approve, then resume — the run drives to COMPLETED.
	if err := wpStore.CompleteApproval(ctx, res.WaitpointToken, true, "u_admin", "lgtm"); err != nil {
		t.Fatalf("complete approval: %v", err)
	}
	resumeAndAwait(t, exec, runStore, res.RunID)

	rec, _ = runStore.Get(ctx, res.RunID)
	if rec.Status != RunStatusCompleted {
		t.Fatalf("after approval+resume status: got %q, want completed", rec.Status)
	}
}

// TestRun_ApprovalDenied_ResolvesRunFailed — a DENIED approval must resume the
// parked run to a terminal FAILED state, never strand it in 'waiting'. Guards
// the "deny doesn't strand the run" contract (resume fires on reject too).
func TestRun_ApprovalDenied_ResolvesRunFailed(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-deny", asyncApprovalLinearDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "WAITING" {
		t.Fatalf("status: got %q, want WAITING", res.Status)
	}

	// Deny the approval, then resume. The run must reach a terminal state.
	if err := wpStore.CompleteApproval(ctx, res.WaitpointToken, false, "u_admin", "nope"); err != nil {
		t.Fatalf("complete (deny): %v", err)
	}
	resumeAndAwait(t, exec, runStore, res.RunID)

	rec, _ := runStore.Get(ctx, res.RunID)
	if rec.Status == RunStatusWaiting {
		t.Fatal("denied run is stranded in 'waiting' — it must resolve to a terminal state")
	}
	if rec.Status != RunStatusFailed {
		t.Fatalf("after denial+resume status: got %q, want failed", rec.Status)
	}
}

const asyncApprovalDAGDSL = `{
  "dsl_version": "1.0",
  "name": "appr-dag",
  "steps": [
    {"id": "draft", "type": "transform", "transform": {"input": "drafted", "expression": "."}},
    {"id": "gate", "type": "wait", "needs": ["draft"], "wait": {"kind": "approval", "approval_prompt": "ship?"}},
    {"id": "final", "type": "transform", "needs": ["gate"], "transform": {"input": "final-{{ steps.draft.output }}", "expression": "."}}
  ]
}`

// TestRunDAG_ApprovalWaitStep — DAG path (needs:). The run suspends at the
// gate (after draft), stamps current_step, returns WAITING; on approval it
// resumes, skips the restored draft, and completes with final output that
// references the restored draft.
func TestRunDAG_ApprovalWaitStep(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-dag", asyncApprovalDAGDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	done := make(chan *RunResult, 1)
	go func() {
		res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
		if err != nil {
			t.Errorf("run: %v", err)
		}
		done <- res
	}()
	var res *RunResult
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("DAG run blocked on the approval instead of returning WAITING")
	}

	if res.Status != "WAITING" || res.CurrentStep != "gate" || res.WaitpointToken == "" {
		t.Fatalf("got status=%q step=%q token=%q, want WAITING/gate/non-empty", res.Status, res.CurrentStep, res.WaitpointToken)
	}
	// current_step stamped on the row (the live empty-current-step bug fix).
	rec, _ := runStore.Get(ctx, res.RunID)
	if rec.CurrentStepID != "gate" {
		t.Errorf("DAG run row current_step: got %q, want gate", rec.CurrentStepID)
	}
	if rec.StepOutputsJSON == "" {
		t.Error("DAG suspend should have persisted the draft output for resume")
	}

	if err := wpStore.CompleteApproval(ctx, res.WaitpointToken, true, "u_admin", ""); err != nil {
		t.Fatalf("complete approval: %v", err)
	}
	resumeAndAwait(t, exec, runStore, res.RunID)

	rec, _ = runStore.Get(ctx, res.RunID)
	if rec.Status != RunStatusCompleted {
		t.Fatalf("after approval+resume status: got %q, want completed (error: %s)", rec.Status, rec.ErrorMessage)
	}
}

// Guard: a NON-foreground-ModeRun caller (here ModeTestRun) must NOT suspend —
// it keeps the existing blocking behaviour. We bound the wait with a short ctx
// so the blocked WaitFor returns and we can assert the result is not WAITING.
func TestRun_TestRunMode_DoesNotSuspend(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()

	store := NewStore(db)
	runStore := NewRunStore(db)
	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	p := saveResumePipeline(t, store, "appr-testrun", asyncApprovalLinearDSL)

	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).
		WithWaitpointStore(wpStore)

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeTestRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status == "WAITING" {
		t.Fatal("ModeTestRun must NOT suspend (no foreground caller to return WAITING to)")
	}
}

// resumeAndAwait runs the approval-resume synchronously (the in-process
// ResumeAfterApproval spawns this on a goroutine; here we drive it directly
// for a deterministic assertion).
func resumeAndAwait(t *testing.T, exec *Executor, runStore *RunStore, runID string) {
	t.Helper()
	ctx := context.Background()
	rec, err := runStore.Get(ctx, runID)
	if err != nil {
		t.Fatalf("load run for resume: %v", err)
	}
	plan, reason := exec.buildResumePlan(ctx, rec)
	if plan == nil {
		t.Fatalf("resume plan nil: %s", reason)
	}
	exec.runResumedRun(ctx, plan, slog.Default())
}
