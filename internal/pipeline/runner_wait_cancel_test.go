package pipeline

import (
	"context"
	"testing"
	"time"
)

// #1426 (3.2) — when a run BLOCKING on a wait(approval) dies (its context is
// cancelled), the waitpoint must flip to 'cancelled' so its inbox approval
// card stops being actionable. Previously the blocking-run death left the
// waitpoint 'pending' — an approve/deny would then resolve a waitpoint whose
// run was already gone.
func TestRunWaitStep_BlockingRunDeath_CancelsWaitpoint(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()

	wpStore := NewSQLWaitpointStore(db)
	defer wpStore.Close()
	exec := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil).
		WithWaitpointStore(wpStore)

	const runID = "run_block_death"
	step := Step{ID: "gate", Type: StepWait, Wait: &WaitStep{Kind: "approval", ApprovalPrompt: "ship?"}}

	// resume=true forces the blocking WaitFor path (not the async park), which
	// is the branch a nested / resumed run takes.
	in := RunInput{WorkspaceID: "ws_test", Mode: ModeRun, resume: true}

	ctx, cancel := context.WithCancel(context.Background())
	// Let CreateApproval + WaitFor start, then kill the run.
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	_, _, _, err := exec.runWaitStep(ctx, step, RenderContext{}, in, runID, 0)
	if err == nil {
		t.Fatalf("expected a cancellation error from the dying blocking run")
	}

	var wpStatus string
	if qerr := db.QueryRow(
		`SELECT status FROM pipeline_waitpoints WHERE pipeline_run_id = ?`, runID).Scan(&wpStatus); qerr != nil {
		t.Fatalf("read waitpoint: %v", qerr)
	}
	if wpStatus != "cancelled" {
		t.Errorf("waitpoint status = %q, want cancelled (blocking run died)", wpStatus)
	}
}
