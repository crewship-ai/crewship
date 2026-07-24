package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
)

// recordingCodeRunner counts RunCode invocations so a test can assert a
// lifecycle hook did (or did not) fire.
type recordingCodeRunner struct{ calls int32 }

func (r *recordingCodeRunner) RunCode(_ context.Context, _ CodeRunRequest) (CodeRunResult, error) {
	atomic.AddInt32(&r.calls, 1)
	return CodeRunResult{Stdout: "true"}, nil
}

// #1426 (2.1) — a user-cancelled run must be labelled CANCELLED everywhere:
// the persisted row is CANCELLED (not FAILED), no failure notification fans
// out, and the on_failure hook does NOT run. Previously the deferred terminal
// write (inside runDSL) fired BEFORE Run() re-labelled the result, so the run
// persisted FAILED, minted an error_fingerprint, paged the failure notifier,
// and ran on_failure.
func TestExecutor_Cancel_ClassifiedEverywhere(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	ctx := context.Background()

	store := NewStore(db)
	runStore := NewRunStore(db)
	registry := NewRunRegistry()

	const runID = "run_cancel_classify"
	runner := runnerFunc(func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		// User presses Cancel while the first step runs.
		_ = registry.Cancel(runID)
		return AgentStepResult{Output: "partial", CostUSD: 0.01}, nil
	})

	var notified int32
	runStore.SetTerminalNotifier(func(_ context.Context, _ string, _ RunStatus) {
		atomic.AddInt32(&notified, 1)
	})
	codeRunner := &recordingCodeRunner{}

	exec := NewExecutor(store, NewResolver(db), runner, nil).
		WithRunStore(runStore).
		WithRunRegistry(registry).
		WithCodeRunner(codeRunner)

	in := validSaveInput("cancel-classify")
	// Two linear agent steps (so the cancel is caught between steps), plus an
	// on_failure code hook that must NOT run on a cancel.
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"cancel-classify","steps":[` +
		`{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"},` +
		`{"id":"s2","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}],` +
		`"hooks":{"on_failure":{"id":"of","type":"code","code":{"runtime":"cel","code":"1 > 0"}}}}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, RunIDOverride: runID,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "CANCELLED" {
		t.Errorf("result status: got %q, want CANCELLED", res.Status)
	}

	rec, gerr := runStore.Get(ctx, runID)
	if gerr != nil {
		t.Fatalf("get run: %v", gerr)
	}
	if rec.Status != RunStatusCancelled {
		t.Errorf("persisted status: got %q, want cancelled", rec.Status)
	}
	if n := atomic.LoadInt32(&notified); n != 0 {
		t.Errorf("failure notifier fired %d times on a cancel; want 0", n)
	}
	if n := atomic.LoadInt32(&codeRunner.calls); n != 0 {
		t.Errorf("on_failure hook ran %d times on a cancel; want 0", n)
	}
}
