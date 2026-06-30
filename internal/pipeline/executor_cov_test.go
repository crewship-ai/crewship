package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// intPtr is a tiny literal-pointer helper for *int validation fields.
func intPtr(n int) *int { return &n }

// ---------------------------------------------------------------------------
// executor.go — Run gateway branches (load/parse errors, idempotency
// dedupe, concurrency gate, cancel re-label), RunDefinition validation,
// runDSL depth ceiling + agentless guard + ctx pre-emption + tier
// override, runStep dispatch, runAgentStep escalation/outcomes paths,
// runCallPipelineStep branches, and the persistRun* projection writes.
// ---------------------------------------------------------------------------

// runnerFunc adapts a closure to the AgentRunner contract for tests
// that need per-call side effects (cancelling, counting).
type runnerFunc func(ctx context.Context, req AgentStepRequest) (AgentStepResult, error)

func (f runnerFunc) RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
	return f(ctx, req)
}

const agentStepDef = `{"dsl_version":"1.0","name":"one-step","steps":[{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}]}`

// openExecutorGateDB returns a store-schema DB that ALSO has the
// idempotency table, so Run's dedupe + concurrency gates can be
// exercised against saved pipelines.
func openExecutorGateDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openStoreTestDB(t)
	if _, err := db.Exec(idempotencySchemaSQL); err != nil {
		t.Fatalf("idempotency schema: %v", err)
	}
	return db
}

func TestExecutor_Run_LoadAndParseErrors(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)
	ctx := context.Background()

	// Unknown pipeline id.
	if _, err := exec.Run(ctx, RunInput{PipelineID: "pln_ghost", WorkspaceID: "ws_test"}); err == nil || !strings.Contains(err.Error(), "load pipeline") {
		t.Errorf("load: %v", err)
	}

	// Stored DSL that doesn't parse.
	in := validSaveInput("bad-dsl")
	in.DefinitionJSON = "this is not json"
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test"}); err == nil || !strings.Contains(err.Error(), "parse stored DSL") {
		t.Errorf("parse: %v", err)
	}
}

// TestExecutor_Run_GovernanceStatusGate is the central airbag: a real run
// (ModeRun) of a non-active routine is refused INSIDE the executor, so the
// status gate holds on EVERY dispatch path (cron / webhook / batch / deferred),
// not just the HTTP handlers. A dry-run is exempt (preview/validate carries no
// persisted status).
// TestRunDefinition_DefaultsToDryRun — the draft-only helper must default an
// unset Mode to the SAFE preview, not live execution, so a caller that forgets
// to set Mode gets a dry-run instead of running real steps with side effects.
func TestRunDefinition_DefaultsToDryRun(t *testing.T) {
	db := openExecutorGateDB(t)
	exec := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "draft",
		Steps:      []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "hi"}},
	}
	// No Mode set — must NOT execute live.
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "DRY_RUN_OK" {
		t.Errorf("status = %q, want DRY_RUN_OK (default must be a safe preview, not ModeRun)", res.Status)
	}
}

func TestExecutor_Run_GovernanceStatusGate(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil)
	ctx := context.Background()

	in := validSaveInput("gated")
	in.DefinitionJSON = agentStepDef
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, status := range []string{"proposed", "disabled"} {
		if err := store.SetStatus(ctx, p.ID, status); err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
		// Real run refused.
		if _, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun}); !errors.Is(err, ErrRoutineNotActive) {
			t.Errorf("status=%s: ModeRun err = %v, want ErrRoutineNotActive", status, err)
		}
		// Dry-run still allowed (no status gate on preview/validate).
		if _, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeDryRun}); errors.Is(err, ErrRoutineNotActive) {
			t.Errorf("status=%s: ModeDryRun must NOT be status-gated, got %v", status, err)
		}
	}

	// Re-activating clears the gate.
	if err := store.SetStatus(ctx, p.ID, "active"); err != nil {
		t.Fatalf("re-activate: %v", err)
	}
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("active run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("active run status = %q, want COMPLETED", res.Status)
	}
}

func TestExecutor_Run_IdempotencyDedupe(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
		WithIdempotencyStore(NewIdempotencyStore(db))
	ctx := context.Background()

	in := validSaveInput("dedupe-me")
	in.DefinitionJSON = agentStepDef
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	first, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, IdempotencyKey: "hook-123",
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Status != "COMPLETED" || first.Deduped {
		t.Fatalf("first run: %+v", first)
	}

	second, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, IdempotencyKey: "hook-123",
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Status != "DEDUPED" || !second.Deduped {
		t.Errorf("second run not deduped: %+v", second)
	}
	if second.RunID != first.RunID {
		t.Errorf("dedupe must return the ORIGINAL run id: %q vs %q", second.RunID, first.RunID)
	}
}

func TestExecutor_Run_ConcurrencyKeyEmpty_ForgetsIdempotency(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
		WithIdempotencyStore(NewIdempotencyStore(db)).
		WithRunRegistry(NewRunRegistry())
	ctx := context.Background()

	in := validSaveInput("gated")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"gated","concurrency_key":"{{ inputs.tenant }}","inputs":[{"name":"tenant"}],"steps":[{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// No tenant input → key renders empty → fail fast.
	_, err = exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, IdempotencyKey: "key-empty",
	})
	if !errors.Is(err, ErrConcurrencyKeyEmpty) {
		t.Fatalf("expected ErrConcurrencyKeyEmpty, got %v", err)
	}

	// The idempotency reservation must have been forgotten — the same
	// key must execute (not dedupe) once the input is supplied.
	res, err := exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		IdempotencyKey: "key-empty", Inputs: map[string]any{"tenant": "acme"},
	})
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if res.Status != "COMPLETED" || res.Deduped {
		t.Errorf("retry after Forget should execute fresh: %+v", res)
	}
}

func TestExecutor_Run_ConcurrencyLimitRejects(t *testing.T) {
	db := openExecutorGateDB(t)
	store := NewStore(db)
	registry := NewRunRegistry()
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).
		WithIdempotencyStore(NewIdempotencyStore(db)).
		WithRunRegistry(registry)
	ctx := context.Background()

	in := validSaveInput("limited")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"limited","concurrency_key":"tenant-fixed","max_concurrent":1,"steps":[{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Occupy the only slot for the rendered key.
	_, release, err := registry.Acquire(ctx, AcquireOpts{
		RunID: "holder", WorkspaceID: "ws_test", PipelineID: p.ID,
		PipelineSlug: "limited", ConcurrencyKey: "tenant-fixed", MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatalf("pre-acquire: %v", err)
	}
	defer release()

	_, err = exec.Run(ctx, RunInput{
		PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun, IdempotencyKey: "key-limited",
	})
	if !errors.Is(err, ErrConcurrencyLimitReached) {
		t.Fatalf("expected ErrConcurrencyLimitReached, got %v", err)
	}
}

func TestExecutor_Run_CancelRelabelsResult(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	registry := NewRunRegistry()
	const runID = "run_cancel_relabel"
	runner := runnerFunc(func(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
		// Simulate a user pressing Cancel while the step runs.
		_ = registry.Cancel(runID)
		return AgentStepResult{Output: "partial"}, nil
	})
	exec := NewExecutor(store, resolver, runner, nil).WithRunRegistry(registry)
	ctx := context.Background()

	in := validSaveInput("cancellable")
	in.DefinitionJSON = agentStepDef
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
		t.Errorf("status: %q, want CANCELLED", res.Status)
	}
	if res.ErrorMessage != "run cancelled" {
		t.Errorf("error message: %q", res.ErrorMessage)
	}
}

func TestExecutor_RunDefinition_Validation(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)
	ctx := context.Background()
	dsl := &DSL{Name: "d", Steps: []Step{{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p"}}}

	if _, err := exec.RunDefinition(ctx, dsl, RunInput{AuthorCrewID: "crew_a"}); err == nil || !strings.Contains(err.Error(), "workspace_id required") {
		t.Errorf("missing workspace: %v", err)
	}
	if _, err := exec.RunDefinition(ctx, dsl, RunInput{WorkspaceID: "ws_test", Mode: ModeRun}); err == nil || !strings.Contains(err.Error(), "author_crew_id required") {
		t.Errorf("missing author crew: %v", err)
	}
}

func TestExecutor_RunDSL_DepthCeiling(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	in := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
		dsl: &DSL{Name: "deep", Steps: []Step{{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p"}}}}
	if _, err := exec.runDSL(context.Background(), in, MaxNestedPipelineDepth); !errors.Is(err, ErrMaxDepthExceeded) {
		t.Errorf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestExecutor_AgentlessRuntimeGuard_RecordsInvocation(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	in := validSaveInput("agentless-violation")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"agentless-violation","agentless":true,"steps":[{"id":"s1","type":"agent_run","agent_slug":"agent_lead","prompt":"go"}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "token-zero guarantee violated") {
		t.Errorf("agentless guard: %+v", res)
	}
	if len(runner.calls) != 0 {
		t.Errorf("agentless violation must not invoke the runner, got %d calls", len(runner.calls))
	}
	// RecordInvocation(FAILED) landed on the pipelines row.
	got, _ := store.GetByID(ctx, p.ID)
	if got.LastInvocationStatus != "FAILED" || got.InvocationCount != 1 {
		t.Errorf("invocation record: status=%q count=%d", got.LastInvocationStatus, got.InvocationCount)
	}
}

func TestExecutor_CtxCancelledBetweenSteps(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	runner := runnerFunc(func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		cancel() // cancel after step 1 completes
		return AgentStepResult{Output: "done-1"}, nil
	})
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "two-steps", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p1"},
		{ID: "s2", Type: StepAgentRun, AgentSlug: "a", Prompt: "p2"},
	}}
	res, err := exec.RunDefinition(ctx, dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || res.FailedAtStep != "s1" {
		t.Errorf("expected FAILED at s1 (last completed), got %+v", res)
	}

	// Pre-cancelled ctx fails before the first step.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	res, err = exec.RunDefinition(ctx2, dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res.Status != "FAILED" || res.FailedAtStep != "s1" {
		t.Errorf("pre-cancelled: %+v", res)
	}
}

func TestExecutor_TierOverride_AppliesToUnpinnedAgentSteps(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "tiered", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "p"},
	}}
	_, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
		TierOverride: ComplexitySmart,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
	}
	// Smart tier maps to the opus default in defaultTier.
	if got := runner.calls[0].Model; !strings.Contains(got, "opus") {
		t.Errorf("tier override not applied: model=%q", got)
	}
}

func TestExecutor_DryRun_CallPipelinePreview(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	dsl := &DSL{Name: "preview", Steps: []Step{
		{ID: "s1", Type: StepCallPipeline, PipelineSlug: "child-pipe"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeDryRun,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Status != "DRY_RUN_OK" || len(res.WouldExecute) != 1 {
		t.Fatalf("dry run result: %+v", res)
	}
	if res.WouldExecute[0].WouldCallSlug != "child-pipe" {
		t.Errorf("WouldCallSlug: %q", res.WouldExecute[0].WouldCallSlug)
	}
}

func TestExecutor_RunStep_DispatchBranches(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)
	ctx := context.Background()
	emit := &pipelineEmitContext{emitter: nopEmitter{}}
	in := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun}
	render := RenderContext{}

	// Unsupported type.
	if _, _, _, err := exec.runStep(ctx, Step{ID: "z", Type: StepType("warp")}, "", AdapterModel{}, nil, in, "r", "p", emit, render, 0); err == nil || !strings.Contains(err.Error(), "unsupported step type") {
		t.Errorf("unsupported type: %v", err)
	}

	// Code step without a wired CodeRunner.
	codeStep := Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "bash", Code: "echo hi"}}
	if _, _, _, err := exec.runStep(ctx, codeStep, "", AdapterModel{}, nil, in, "r", "p", emit, render, 0); err == nil {
		t.Error("code step without runner must error")
	}

	// HTTP step against a private address with SSRF guards ON.
	httpStep := Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "http://127.0.0.1:1/x"}}
	if _, _, _, err := exec.runStep(ctx, httpStep, "", AdapterModel{}, nil, in, "r", "p", emit, render, 0); err == nil {
		t.Error("http step to loopback must be blocked by the SSRF guard")
	}
}

// seedTierFallback installs a workspace tier mapping with a fallback
// entry so escalation chains have a second attempt to walk.
func seedTierFallback(t *testing.T, store *Store) {
	t.Helper()
	// The store shares the workspaces table from openStoreTestDB.
	_, err := store.db.Exec(`UPDATE workspaces SET execution_tiers_json = ? WHERE id = 'ws_test'`,
		`{"moderate":{"primary":{"adapter":"claude","model":"m-primary"},"fallback":[{"adapter":"claude","model":"m-fallback"}]}}`)
	if err != nil {
		t.Fatalf("seed tiers: %v", err)
	}
}

func TestExecutor_RunnerError_EscalatesToFallbackTier(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	seedTierFallback(t, store)
	runner := newMockRunner()
	// First attempt fails hard (non-transient), second succeeds.
	runner.errBySlug["worker"] = []error{errors.New("hard failure: model unavailable")}
	runner.outputsBySlug["worker"] = []string{"recovered on fallback"}
	emitted := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, emitted)

	dsl := &DSL{Name: "escalating", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" || res.Output != "recovered on fallback" {
		t.Fatalf("escalation result: %+v", res)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(runner.calls))
	}
	if runner.calls[0].Model != "m-primary" || runner.calls[1].Model != "m-fallback" {
		t.Errorf("tier chain: %q then %q", runner.calls[0].Model, runner.calls[1].Model)
	}
}

func TestExecutor_Validation_RetryStepNotImplemented(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["worker"] = []string{"short"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "retry-step", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			OnFail:     OnFailRetryStep,
			Validation: &Validation{MinLength: intPtr(100)}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "retry_step not yet implemented") {
		t.Errorf("retry_step: %+v", res)
	}
}

func TestExecutor_Validation_ExhaustsTiers(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	seedTierFallback(t, store)
	runner := newMockRunner()
	runner.outputsBySlug["worker"] = []string{"short", "also short"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "exhausting", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			OnFail:     OnFailEscalateTier,
			Validation: &Validation{MinLength: intPtr(500)}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "exhausting tiers") {
		t.Errorf("exhaust: %+v", res)
	}
	if len(runner.calls) != 2 {
		t.Errorf("expected both tiers attempted, got %d", len(runner.calls))
	}
	// The fallback attempt must carry the validation feedback block.
	if !strings.Contains(runner.calls[1].Prompt, "PREVIOUS ATTEMPT FAILED VALIDATION") {
		t.Errorf("feedback not injected: %q", runner.calls[1].Prompt)
	}
}

func TestExecutor_Outcomes_Paths(t *testing.T) {
	mkExec := func(t *testing.T, graderOutput string, graderErr error) (*Executor, *mockRunner, func()) {
		store, resolver, cleanup := openExecutorTestDB(t)
		runner := newMockRunner()
		runner.outputsBySlug["worker"] = []string{"worker says hi", "worker retry"}
		if graderErr != nil {
			runner.errBySlug["grader"] = []error{graderErr}
		} else {
			runner.outputsBySlug["grader"] = []string{graderOutput, graderOutput}
		}
		return NewExecutor(store, resolver, runner, nil), runner, cleanup
	}
	mkDSL := func(onFail OnFailAction) *DSL {
		return &DSL{Name: "graded", Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
				Outcomes: &Outcomes{
					GraderAgentSlug: "grader",
					Criteria:        []OutcomeCriterion{{Name: "tone", Rule: "be nice"}},
					OnFail:          onFail,
				}},
		}}
	}
	runIt := func(t *testing.T, exec *Executor, dsl *DSL) *RunResult {
		t.Helper()
		res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
			WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return res
	}

	t.Run("grader pass", func(t *testing.T) {
		exec, runner, cleanup := mkExec(t, `{"passed":true,"per_criterion":{"tone":true},"feedback":""}`, nil)
		defer cleanup()
		res := runIt(t, exec, mkDSL(""))
		if res.Status != "COMPLETED" || res.Output != "worker says hi" {
			t.Errorf("pass: %+v", res)
		}
		if len(runner.calls) != 2 { // worker + grader
			t.Errorf("calls: %d", len(runner.calls))
		}
	})

	t.Run("grader infrastructure error returns worker output", func(t *testing.T) {
		exec, _, cleanup := mkExec(t, "", errors.New("grader fatal: container gone"))
		defer cleanup()
		res := runIt(t, exec, mkDSL(""))
		if res.Status != "COMPLETED" || res.Output != "worker says hi" {
			t.Errorf("grader error must be non-fatal: %+v", res)
		}
	})

	t.Run("grader reject with abort", func(t *testing.T) {
		exec, _, cleanup := mkExec(t, `{"passed":false,"per_criterion":{"tone":false},"feedback":"too rude"}`, nil)
		defer cleanup()
		res := runIt(t, exec, mkDSL(OnFailAbort))
		if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "outcomes failed") {
			t.Errorf("abort: %+v", res)
		}
	})

	t.Run("grader reject with retry_step degrades to abort", func(t *testing.T) {
		exec, _, cleanup := mkExec(t, `{"passed":false,"per_criterion":{"tone":false},"feedback":"too rude"}`, nil)
		defer cleanup()
		res := runIt(t, exec, mkDSL(OnFailRetryStep))
		if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "retry_step requires per-step budget") {
			t.Errorf("retry_step: %+v", res)
		}
	})

	t.Run("grader reject with escalate exhausts tiers", func(t *testing.T) {
		exec, _, cleanup := mkExec(t, `{"passed":false,"per_criterion":{"tone":false},"feedback":"too rude"}`, nil)
		defer cleanup()
		res := runIt(t, exec, mkDSL(OnFailEscalateTier))
		if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "exhausting tiers") {
			t.Errorf("escalate: %+v", res)
		}
	})
}

func TestExecutor_CallPipeline_NestedBranches(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["child-agent"] = []string{"child-output"}
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	// Target with unparseable DSL.
	badTarget := fakePipeline(t, "bad-child", "not json", "crew_a", "agent_lead")
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "bad-child" {
			return badTarget, nil
		}
		if slug == "good-child" {
			return fakePipeline(t, "good-child",
				`{"dsl_version":"1.0","name":"good-child","inputs":[{"name":"a"},{"name":"b"}],"steps":[{"id":"c1","type":"agent_run","agent_slug":"child-agent","prompt":"a={{ inputs.a }}"}]}`,
				"crew_a", "agent_lead"), nil
		}
		return nil, errors.New("backend exploded")
	}))

	parent := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun}
	render := RenderContext{Inputs: map[string]any{"x": "rendered-val"}}

	if _, _, _, err := exec.runCallPipelineStep(ctx, Step{ID: "s", Type: StepCallPipeline, PipelineSlug: "bad-child"}, parent, render, 0); err == nil || !strings.Contains(err.Error(), "parse target") {
		t.Errorf("bad child: %v", err)
	}

	// Generic lookup error (not ErrNotFound).
	if _, _, _, err := exec.runCallPipelineStep(ctx, Step{ID: "s", Type: StepCallPipeline, PipelineSlug: "ghost"}, parent, render, 0); err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Errorf("lookup error: %v", err)
	}

	// Successful nested call: string nested inputs render against the
	// parent context, non-strings pass through verbatim.
	out, _, _, err := exec.runCallPipelineStep(ctx, Step{
		ID: "s", Type: StepCallPipeline, PipelineSlug: "good-child",
		NestedInputs: map[string]any{"a": "{{ inputs.x }}", "b": 42},
	}, parent, render, 0)
	if err != nil {
		t.Fatalf("nested run: %v", err)
	}
	if out != "child-output" {
		t.Errorf("nested output: %q", out)
	}
	if len(runner.calls) != 1 || runner.calls[0].Prompt != "a=rendered-val" {
		t.Errorf("nested input render: %+v", runner.calls)
	}
}

// runsProjectionDDL is the pipeline_runs table for executors wired
// with a RunStore in these tests (FK-free variant of the v83 schema).
const runsProjectionDDL = `
CREATE TABLE IF NOT EXISTS pipeline_runs (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL,
    pipeline_id         TEXT NOT NULL,
    pipeline_slug       TEXT NOT NULL,
    pipeline_version    INTEGER,
    definition_hash     TEXT,
    status              TEXT NOT NULL,
    mode                TEXT NOT NULL DEFAULT 'run',
    started_at          TEXT NOT NULL,
    ended_at            TEXT,
    current_step_id     TEXT,
    step_outputs_json   TEXT NOT NULL DEFAULT '{}',
    output              TEXT,
    cost_usd            REAL NOT NULL DEFAULT 0,
    duration_ms         INTEGER NOT NULL DEFAULT 0,
    error_message       TEXT,
    failed_at_step      TEXT,
    error_fingerprint   TEXT,
    invoking_crew_id    TEXT,
    invoking_agent_id   TEXT,
    invoking_user_id    TEXT,
    triggered_via       TEXT NOT NULL DEFAULT 'manual',
    triggered_by_id     TEXT,
    idempotency_key     TEXT,
    inputs_json         TEXT NOT NULL DEFAULT '{}',
    concurrency_key     TEXT,
    metadata_json       TEXT NOT NULL DEFAULT '{}',
    is_replay           INTEGER NOT NULL DEFAULT 0,
    replay_of           TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);`

func TestExecutor_RunStorePersistence_FullLifecycle(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	store := NewStore(db)
	runStore := NewRunStore(db)
	exec := NewExecutor(store, NewResolver(db), newMockRunner(), nil).WithRunStore(runStore)
	ctx := context.Background()

	in := validSaveInput("persisted")
	in.DefinitionJSON = agentStepDef
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %q", res.Status)
	}

	rec, err := runStore.Get(ctx, res.RunID)
	if err != nil {
		t.Fatalf("run row: %v", err)
	}
	if rec.Status != RunStatusCompleted {
		t.Errorf("persisted status: %q", rec.Status)
	}
	if rec.DefinitionHash != p.DefinitionHash {
		t.Errorf("definition hash not stamped: %q", rec.DefinitionHash)
	}
	if !strings.Contains(rec.StepOutputsJSON, "s1") {
		t.Errorf("step outputs not flushed: %q", rec.StepOutputsJSON)
	}
}

func TestExecutor_RunStorePersistence_BrokenProjectionIsNonFatal(t *testing.T) {
	// Wire a RunStore whose table doesn't exist: every projection write
	// fails, persistWarn fires, and the run still completes.
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithRunStore(NewRunStore(store.db))
	ctx := context.Background()

	in := validSaveInput("broken-projection")
	in.DefinitionJSON = agentStepDef
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run must survive projection failures: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %q", res.Status)
	}
}

func TestExecutor_PersistRunStartAndWarn_EdgeBranches(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	runStore := NewRunStore(db)
	exec := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil).WithRunStore(runStore)
	ctx := context.Background()
	in := RunInput{WorkspaceID: "ws_test"}

	// nil inputs map marshals to "null" → stored as "{}".
	exec.persistRunStart(ctx, in, "rs-null", "pln_x", "x", nil, time.Now())
	rec, err := runStore.Get(ctx, "rs-null")
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if rec.InputsJSON != "{}" {
		t.Errorf("nil inputs should persist {}, got %q", rec.InputsJSON)
	}

	// Empty pipelineID (draft) skips the insert entirely.
	exec.persistRunStart(ctx, in, "rs-draft", "", "x", nil, time.Now())
	if _, err := runStore.Get(ctx, "rs-draft"); !errors.Is(err, ErrRunNotFoundInStore) {
		t.Errorf("draft should not insert: %v", err)
	}

	// No RunStore wired → no-op, no panic.
	bare := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil)
	bare.persistRunStart(ctx, in, "rs-none", "pln_x", "x", nil, time.Now())
	bare.persistRunTerminal(ctx, "rs-none", in, "pln_x", &RunResult{Status: "COMPLETED"}, time.Now())

	// persistWarn's nil-error early return.
	exec.persistWarn("stage", "run", nil)
}

func TestExecutor_PersistRunTerminal_StatusMapping(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	runStore := NewRunStore(db)
	exec := NewExecutor(NewStore(db), NewResolver(db), newMockRunner(), nil).WithRunStore(runStore)
	ctx := context.Background()

	mkRow := func(id string) {
		t.Helper()
		if err := runStore.Insert(ctx, &RunRecord{ID: id, WorkspaceID: "ws_test", PipelineID: "pln_x", PipelineSlug: "x", Status: RunStatusRunning}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	status := func(id string) RunStatus {
		t.Helper()
		rec, err := runStore.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		return rec.Status
	}
	in := RunInput{WorkspaceID: "ws_test"}
	start := time.Now()

	// nil result + live ctx → failed with generic message.
	mkRow("t-nil")
	exec.persistRunTerminal(ctx, "t-nil", in, "pln_x", nil, start)
	if got := status("t-nil"); got != RunStatusFailed {
		t.Errorf("nil result: %q", got)
	}

	// nil result + cancelled ctx → cancelled.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	mkRow("t-nil-cancel")
	exec.persistRunTerminal(cctx, "t-nil-cancel", in, "pln_x", nil, start)
	if got := status("t-nil-cancel"); got != RunStatusCancelled {
		t.Errorf("nil+cancel: %q", got)
	}

	// Explicit status mapping.
	cases := map[string]RunStatus{
		"CANCELLED":  RunStatusCancelled,
		"DRY_RUN_OK": RunStatusDryRunOK,
		"BOGUS":      RunStatusFailed, // unknown terminal status
	}
	for resultStatus, want := range cases {
		id := "t-" + strings.ToLower(resultStatus)
		mkRow(id)
		exec.persistRunTerminal(ctx, id, in, "pln_x", &RunResult{RunID: id, Status: resultStatus}, start)
		if got := status(id); got != want {
			t.Errorf("%s → %q, want %q", resultStatus, got, want)
		}
	}

	// DEDUPED leaves the row untouched.
	mkRow("t-deduped")
	exec.persistRunTerminal(ctx, "t-deduped", in, "pln_x", &RunResult{RunID: "t-deduped", Status: "DEDUPED"}, start)
	if got := status("t-deduped"); got != RunStatusRunning {
		t.Errorf("DEDUPED must not transition the row: %q", got)
	}

	// Empty pipelineID (draft) → no-op even with a result.
	exec.persistRunTerminal(ctx, "t-draft", in, "", &RunResult{Status: "COMPLETED"}, start)
	if _, err := runStore.Get(ctx, "t-draft"); !errors.Is(err, ErrRunNotFoundInStore) {
		t.Errorf("draft path must not write a row: %v", err)
	}
}
