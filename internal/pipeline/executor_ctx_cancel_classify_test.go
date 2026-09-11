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
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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

// The other half of the contract: what must NOT become a cancellation.
//
// runWasCancelled widens the classification from "the registry says so" to
// "the registry says so, or the run's own context was cancelled". A widened
// classifier is only safe if the things it now catches are all really
// cancellations — so each test below drives a DIFFERENT way a run can end
// badly and pins that it still reads FAILED, with a fingerprint, a failure
// notification and the on_failure hook, exactly as before.

// classifyOutcome is one run driven to a terminal row, with everything the
// classification decides read back off it.
type classifyOutcome struct {
	rec      *RunRecord
	notified int32
	hookRuns int32
	journal  []string
}

// runToTerminal saves `definitionJSON`, runs it with the supplied runner on a
// context the test controls, and reports the persisted row plus the side
// effects the FAILED/CANCELLED split governs.
func runToTerminal(t *testing.T, slug, definitionJSON string, runner AgentRunner, ctx context.Context) classifyOutcome {
	t.Helper()
	db := openResumeTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db)
	runStore := NewRunStore(db)
	em := &captureEmitter{}

	var notified int32
	runStore.SetTerminalNotifier(func(_ context.Context, _ string, _ RunStatus) {
		atomic.AddInt32(&notified, 1)
	})
	hookRuns := &recordingCodeRunner{}

	exec := NewExecutor(store, NewResolver(db), runner, em).
		WithRunStore(runStore).
		WithRunRegistry(NewRunRegistry()).
		WithCodeRunner(hookRuns)
	exec.SetAllowPrivateHTTPForTesting(true)

	in := validSaveInput(slug)
	in.DefinitionJSON = definitionJSON
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rec, err := runStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	var types []string
	for _, e := range em.entries {
		types = append(types, string(e.Type))
	}
	return classifyOutcome{
		rec:      rec,
		notified: atomic.LoadInt32(&notified),
		hookRuns: atomic.LoadInt32(&hookRuns.calls),
		journal:  types,
	}
}

func failingStepDSL(slug, extra string) string {
	return `{"dsl_version":"1.0","name":"` + slug + `","steps":[` + extra + `],` +
		`"hooks":{"on_failure":{"id":"of","type":"code","code":{"runtime":"cel","code":"1 > 0"}}}}`
}

// TestClassify_RealStepErrorStaysFailed is the baseline the widening must not
// disturb: a step that errors on its own merits, on a healthy context.
func TestClassify_RealStepErrorStaysFailed(t *testing.T) {
	runner := runnerFunc(func(context.Context, AgentStepRequest) (AgentStepResult, error) {
		return AgentStepResult{}, errors.New("the model refused")
	})
	got := runToTerminal(t, "classify-real-error",
		failingStepDSL("classify-real-error", `{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}`),
		runner, context.Background())

	if got.rec.Status != RunStatusFailed {
		t.Errorf("status = %q, want failed — nothing cancelled this run", got.rec.Status)
	}
	if got.rec.ErrorFingerprint == "" {
		t.Error("a real failure must still be fingerprinted for the errors view")
	}
	if got.notified != 1 {
		t.Errorf("failure notifier fired %d times, want 1", got.notified)
	}
	if got.hookRuns != 1 {
		t.Errorf("on_failure hook ran %d times, want 1", got.hookRuns)
	}
}

// TestClassify_StepTimeoutStaysFailed — a step that blew its own
// `timeout_seconds`. The deadline belongs to the step, not to the run, so the
// run's context is untouched and the verdict must stay a failure: the work did
// not finish in the time its author allowed, which is not a cancellation and
// is exactly the kind of thing an errors view should group.
func TestClassify_StepTimeoutStaysFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	step := `{"id":"s1","type":"http","timeout_seconds":1,"http":{"method":"GET","url":"` + srv.URL + `"}}`
	got := runToTerminal(t, "classify-step-timeout",
		failingStepDSL("classify-step-timeout", step), newMockRunner(), context.Background())

	if got.rec.Status != RunStatusFailed {
		t.Errorf("status = %q, want failed — a step's own deadline is not a cancellation", got.rec.Status)
	}
	if got.rec.ErrorFingerprint == "" {
		t.Error("a step timeout must stay fingerprinted")
	}
	if got.notified != 1 || got.hookRuns != 1 {
		t.Errorf("notifier=%d on_failure=%d, want 1/1", got.notified, got.hookRuns)
	}
}

// TestClassify_RunDeadlineExceededStaysFailed — the run's OWN context carried
// a deadline and blew it. ctx.Err() is DeadlineExceeded, not Canceled, and
// runWasCancelled excludes it deliberately: a run that ran out of the time it
// was given failed on the merits. If this ever flips to cancelled, every
// timed-out run silently leaves the errors view.
func TestClassify_RunDeadlineExceededStaysFailed(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		<-ctx.Done()
		return AgentStepResult{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	got := runToTerminal(t, "classify-run-deadline",
		failingStepDSL("classify-run-deadline", `{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}`),
		runner, ctx)

	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("fixture did not produce a deadline: %v", ctx.Err())
	}
	if got.rec.Status != RunStatusFailed {
		t.Errorf("status = %q, want failed — DeadlineExceeded is not a cancellation", got.rec.Status)
	}
	if got.rec.ErrorFingerprint == "" {
		t.Error("a run that exceeded its own deadline must stay fingerprinted")
	}
	if got.notified != 1 || got.hookRuns != 1 {
		t.Errorf("notifier=%d on_failure=%d, want 1/1", got.notified, got.hookRuns)
	}
}

// TestClassify_CancelledRunSuppressesFailureMachinery is the positive side
// stated as one assertion set, including the journal: the run row, the
// outcome, the fingerprint, the notifier, the hook and the emitted entry must
// all describe the same event.
func TestClassify_CancelledRunSuppressesFailureMachinery(t *testing.T) {
	runner := runnerFunc(func(ctx context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		<-ctx.Done()
		return AgentStepResult{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	got := runToTerminal(t, "classify-cancelled",
		failingStepDSL("classify-cancelled", `{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}`),
		runner, ctx)

	if got.rec.Status != RunStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.rec.Status)
	}
	if got.rec.Outcome == "FAILED" {
		t.Errorf("outcome = %q on a cancelled run", got.rec.Outcome)
	}
	if got.rec.ErrorFingerprint != "" {
		t.Errorf("fingerprint %q minted for a cancel", got.rec.ErrorFingerprint)
	}
	if got.notified != 0 {
		t.Errorf("failure notifier fired %d times for a cancel, want 0", got.notified)
	}
	if got.hookRuns != 0 {
		t.Errorf("on_failure ran %d times for a cancel, want 0", got.hookRuns)
	}
	// The journal's own classification predates this change and reads
	// ctx.Err(); the row now agrees with it instead of contradicting it.
	if !slices.Contains(got.journal, "pipeline.run.failed") {
		t.Errorf("journal entries %v — the run-level entry is missing entirely", got.journal)
	}
}

// TestClassify_ApprovalTimeoutStaysFailed — a waitpoint that expired. The
// verdict came from the timeout sweeper, the run's context is healthy, and
// "nobody answered in time" is a failure of the routine's design or its
// operators, not an operator stopping the run.
func TestClassify_ApprovalTimeoutStaysFailed(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	store := NewStore(db)
	runStore := NewRunStore(db)
	waits := NewSQLWaitpointStore(db)
	t.Cleanup(waits.Close)

	var notified int32
	runStore.SetTerminalNotifier(func(_ context.Context, _ string, _ RunStatus) {
		atomic.AddInt32(&notified, 1)
	})
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).
		WithRunStore(runStore).
		WithWaitpointStore(waits)

	in := validSaveInput("classify-approval-timeout")
	// depth>0 semantics: no run row to park, so the wait blocks and resolves
	// from the expired waitpoint rather than suspending. timeout_seconds is
	// already in the past by the time WaitFor reads it.
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"classify-approval-timeout","steps":[` +
		`{"id":"gate","type":"wait","timeout_seconds":1,"wait":{"kind":"approval","approval_prompt":"ok?"}}]}`
	p, err := store.Save(context.Background(), in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// A parked run returns WAITING; drive the sweep and resume the same way
	// production does, then read the terminal row.
	if res.Status == "WAITING" {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if _, _, err := waits.RecoverPending(context.Background()); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			exec.ResumeAfterApproval(res.RunID, slog.New(slog.NewTextHandler(io.Discard, nil)))
			rec, gerr := runStore.Get(context.Background(), res.RunID)
			if gerr == nil && rec.Status != RunStatusWaiting && rec.Status != RunStatusRunning {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	rec, err := runStore.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rec.Status != RunStatusFailed {
		t.Errorf("status = %q, want failed — an unanswered approval is not a cancellation", rec.Status)
	}
	if !strings.Contains(rec.ErrorMessage, "timed out") {
		t.Errorf("error_message = %q, want it to say the approval timed out", rec.ErrorMessage)
	}
}
