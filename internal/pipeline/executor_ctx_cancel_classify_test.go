package pipeline

// #1426 item 2.1 says a cancelled run must be labelled CANCELLED everywhere.
// TestExecutor_Cancel_ClassifiedEverywhere proves that for ONE cause: a user
// pressing Cancel, which routes through the RunRegistry. The classification
// defer in runDSL is gated on exactly that — e.runs.IsCancelRequested — so
// every OTHER way a run's context dies keeps the FAILED label the step loop
// wrote on its way out.
//
// The other ways are not exotic. The default POST .../run path passes
// r.Context() straight to exec.Run, so a caller hanging up mid-run (a closed
// tab, a proxy timeout, the CLI's own per-call deadline) cancels the run
// through a context the registry has never heard of. What lands is a run row
// reading status=failed, outcome=FAILED, error_message="context canceled",
// carrying an error_fingerprint into the errors view, having paged the
// failure notifier and having run the on_failure hook — while the journal
// entry for the same instant already says CANCELLED, because emitRunFailed
// classifies off ctx.Err() rather than off the registry.
//
// These tests pin the cause-independent contract: cancellation is cancellation
// whichever context carried it.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cancelClassifyRig saves a two-step routine whose first step blocks until
// the test cancels the run's context from outside the RunRegistry.
type cancelClassifyRig struct {
	exec     *Executor
	runStore *RunStore
	pipeline *Pipeline
	notified *int32
	hookRuns *recordingCodeRunner
}

func newCancelClassifyRig(t *testing.T, slug string, withRegistry bool) *cancelClassifyRig {
	t.Helper()
	db := openResumeTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	runStore := NewRunStore(db)

	runner := runnerFunc(func(ctx context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		<-ctx.Done() // the caller went away mid-step
		return AgentStepResult{}, ctx.Err()
	})

	var notified int32
	runStore.SetTerminalNotifier(func(_ context.Context, _ string, _ RunStatus) {
		atomic.AddInt32(&notified, 1)
	})
	hookRuns := &recordingCodeRunner{}

	exec := NewExecutor(store, NewResolver(db), runner, nil).
		WithRunStore(runStore).
		WithCodeRunner(hookRuns)
	if withRegistry {
		exec = exec.WithRunRegistry(NewRunRegistry())
	}

	in := validSaveInput(slug)
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"` + slug + `","steps":[` +
		`{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"},` +
		`{"id":"s2","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}],` +
		`"hooks":{"on_failure":{"id":"of","type":"code","code":{"runtime":"cel","code":"1 > 0"}}}}`
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	return &cancelClassifyRig{exec: exec, runStore: runStore, pipeline: p, notified: &notified, hookRuns: hookRuns}
}

// runUntilCancelled starts the run, waits for the row to exist, cancels the
// context the CALLER owns (never the registry), and returns the final row.
func (r *cancelClassifyRig) runUntilCancelled(t *testing.T) *RunRecord {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *RunResult, 1)
	go func() {
		res, _ := r.exec.Run(ctx, RunInput{
			PipelineID: r.pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		})
		done <- res
	}()

	var runID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && runID == "" {
		recs, err := r.runStore.ListInFlight(context.Background())
		if err == nil && len(recs) == 1 && recs[0].CurrentStepID == "s1" {
			runID = recs[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runID == "" {
		cancel()
		t.Fatal("the run never reached its first step")
	}
	cancel() // the caller hangs up

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
	rec, err := r.runStore.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	return rec
}

// TestExecutor_CallerHangsUpMidStep_IsCancelledNotFailed is the row a caller
// disconnect leaves behind. Everything asserted here is what
// TestExecutor_Cancel_ClassifiedEverywhere already asserts for the registry
// cause — same contract, different context.
func TestExecutor_CallerHangsUpMidStep_IsCancelledNotFailed(t *testing.T) {
	rig := newCancelClassifyRig(t, "ctx-cancel-classify", true)
	rec := rig.runUntilCancelled(t)

	if rec.Status != RunStatusCancelled {
		t.Errorf("status = %q, want %q — the run ended because its caller went away, which is a cancellation whatever context carried it",
			rec.Status, RunStatusCancelled)
	}
	if rec.Outcome == "FAILED" {
		t.Errorf("outcome = %q; a cancelled run is not a failed one", rec.Outcome)
	}
	if rec.ErrorFingerprint != "" {
		t.Errorf("error_fingerprint = %q; a cancel must not be grouped and bulk-replayed as a bug", rec.ErrorFingerprint)
	}
	if n := atomic.LoadInt32(rig.notified); n != 0 {
		t.Errorf("failure notifier fired %d times for a cancelled run, want 0", n)
	}
	if n := atomic.LoadInt32(&rig.hookRuns.calls); n != 0 {
		t.Errorf("on_failure hook ran %d times for a cancelled run, want 0", n)
	}
}

// TestExecutor_CallerHangsUpMidStep_ReasonIsReadable is the §9 legibility
// half: "context canceled" is the Go sentinel, not an explanation, and a run
// detail that shows it tells the reader nothing about what happened or
// whether the step's work took effect.
func TestExecutor_CallerHangsUpMidStep_ReasonIsReadable(t *testing.T) {
	rig := newCancelClassifyRig(t, "ctx-cancel-reason", true)
	rec := rig.runUntilCancelled(t)

	if rec.ErrorMessage == "" {
		t.Fatal("no reason recorded at all")
	}
	if rec.ErrorMessage == context.Canceled.Error() {
		t.Errorf("error_message = %q — the bare Go sentinel names neither the cause nor the step", rec.ErrorMessage)
	}
	if !strings.Contains(strings.ToLower(rec.ErrorMessage), "cancel") {
		t.Errorf("error_message = %q does not say the run was cancelled", rec.ErrorMessage)
	}
	if !strings.Contains(rec.ErrorMessage, "s1") {
		t.Errorf("error_message = %q does not say where the run stopped", rec.ErrorMessage)
	}
	if rec.FailedAtStep != "s1" {
		t.Errorf("failed_at_step = %q, want \"s1\" — the reader still needs to know where it stopped", rec.FailedAtStep)
	}
}

// TestExecutor_CancelWithoutRegistry_StillClassified removes the RunRegistry
// entirely: a nested or background executor wired without one has no
// IsCancelRequested to consult, so the classification cannot depend on it.
func TestExecutor_CancelWithoutRegistry_StillClassified(t *testing.T) {
	rig := newCancelClassifyRig(t, "ctx-cancel-noregistry", false)
	rec := rig.runUntilCancelled(t)

	if rec.Status != RunStatusCancelled {
		t.Errorf("status = %q, want %q with no registry wired", rec.Status, RunStatusCancelled)
	}
	if rec.ErrorFingerprint != "" {
		t.Errorf("error_fingerprint = %q on a registry-less cancel", rec.ErrorFingerprint)
	}
}
