package pipeline

// Tests for the completeRun/failRun → maybeGenerateRunVerdict wiring
// (#1403). Exercises the GATING logic (agentless / dry-run / ctx-
// cancelled skip) with a recording WithRunVerdict hook — the hook's
// own behavior (feature flag + entry fetch + LLM call) is covered by
// newRunVerdictHook's own package (internal/runverdict) and
// TestNewWiredExecutor_WiresEveryDependency for the factory wiring.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingVerdictHook is a WithRunVerdict closure that records every
// invocation, for asserting the gating logic in maybeGenerateRunVerdict.
type recordingVerdictHook struct {
	mu    sync.Mutex
	calls []string // runID per call
}

func (r *recordingVerdictHook) fn(ctx context.Context, workspaceID, crewID, agentID, pipelineID, pipelineSlug, runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, runID)
}

func (r *recordingVerdictHook) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestMaybeGenerateRunVerdict_FiresOnNormalCompletion(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	hook := &recordingVerdictHook{}
	exec := NewExecutor(store, resolver, newMockRunner(), &captureEmitter{}).WithRunVerdict(hook.fn)

	dsl := &DSL{DSLVersion: "1.0", Name: "one-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: got %q", res.Status)
	}
	exec.verdictWG.Wait()
	if got := hook.count(); got != 1 {
		t.Errorf("verdict hook calls = %d, want 1", got)
	}
}

func TestMaybeGenerateRunVerdict_FiresOnFailure(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	hook := &recordingVerdictHook{}
	runner := runnerFunc(func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		return AgentStepResult{}, errors.New("boom")
	})
	exec := NewExecutor(store, resolver, runner, &captureEmitter{}).WithRunVerdict(hook.fn)

	dsl := &DSL{DSLVersion: "1.0", Name: "one-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status: got %q, want FAILED", res.Status)
	}
	exec.verdictWG.Wait()
	if got := hook.count(); got != 1 {
		t.Errorf("verdict hook calls = %d, want 1 (failures get a verdict too)", got)
	}
}

func TestMaybeGenerateRunVerdict_SkippedWhenAgentless(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	hook := &recordingVerdictHook{}
	exec := NewExecutor(store, resolver, newMockRunner(), &captureEmitter{}).WithRunVerdict(hook.fn)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Agentless:  true,
		Steps: []Step{
			{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: "true", Expression: "."}},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: got %q", res.Status)
	}
	exec.verdictWG.Wait()
	if got := hook.count(); got != 0 {
		t.Errorf("verdict hook calls = %d, want 0 (agentless runs skip verdict generation — token-zero guarantee)", got)
	}
}

func TestMaybeGenerateRunVerdict_SkippedOnDryRun(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	hook := &recordingVerdictHook{}
	exec := NewExecutor(store, resolver, newMockRunner(), &captureEmitter{}).WithRunVerdict(hook.fn)

	dsl := &DSL{DSLVersion: "1.0", Name: "one-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeDryRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "DRY_RUN_OK" {
		t.Fatalf("status: got %q, want DRY_RUN_OK", res.Status)
	}
	exec.verdictWG.Wait()
	if got := hook.count(); got != 0 {
		t.Errorf("verdict hook calls = %d, want 0 (dry runs are previews, not real runs)", got)
	}
}

// TestDrainVerdicts_WaitsForInFlightOnSharedWG proves the shutdown-drain
// contract: an in-flight verdict goroutine — registered on a shared
// WaitGroup wired via WithSharedVerdictWaitGroup — is NOT lost. A bounded
// DrainVerdicts times out (returns false) while the verdict is still
// running, and returns true once it finishes. This is the real bug from
// finding 5a: without draining, the process could exit and drop the
// mid-flight verdict.
func TestDrainVerdicts_WaitsForInFlightOnSharedWG(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()

	release := make(chan struct{})
	started := make(chan struct{})
	blockingHook := func(ctx context.Context, workspaceID, crewID, agentID, pipelineID, pipelineSlug, runID string) {
		close(started)
		<-release // hold the verdict "in flight" until the test releases it
	}

	shared := &sync.WaitGroup{}
	exec := NewExecutor(store, resolver, newMockRunner(), &captureEmitter{}).
		WithRunVerdict(blockingHook).
		WithSharedVerdictWaitGroup(shared)

	dsl := &DSL{DSLVersion: "1.0", Name: "one-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
	}}
	if _, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The verdict goroutine must have registered on the SHARED group, not
	// the executor-local one.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("verdict hook never started")
	}

	// While it's blocked, a bounded drain must report NOT-drained.
	if WaitTimeout(shared, 50*time.Millisecond) {
		t.Fatal("DrainVerdicts returned true while a verdict was still in flight")
	}

	// Release it; now the drain must complete.
	close(release)
	if !WaitTimeout(shared, 2*time.Second) {
		t.Fatal("DrainVerdicts did not drain after the verdict finished")
	}
	// exec.DrainVerdicts routes to the shared group too.
	if !exec.DrainVerdicts(time.Second) {
		t.Fatal("exec.DrainVerdicts did not observe the drained shared group")
	}
}

func TestMaybeGenerateRunVerdict_NilHookIsNoop(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	// No WithRunVerdict call — mirrors an executor built without the
	// run_summary aux slot configured.
	exec := NewExecutor(store, resolver, newMockRunner(), &captureEmitter{})

	dsl := &DSL{DSLVersion: "1.0", Name: "one-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: got %q", res.Status)
	}
	exec.verdictWG.Wait() // must not panic on a nil-hook executor
}
