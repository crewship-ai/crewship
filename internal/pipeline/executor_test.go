package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// mockRunner is a deterministic AgentRunner that returns canned
// outputs based on the step the test set up. Captures every call so
// assertions can verify the right agent_slug + adapter + model
// reached the runner.
type mockRunner struct {
	mu    sync.Mutex
	calls []AgentStepRequest
	// outputsBySlug maps agent_slug → output to return. If a slug
	// has multiple entries, they're returned in order (one per call)
	// — enables tier-escalation tests where the same slug is hit
	// across multiple tiers.
	outputsBySlug map[string][]string
	// errBySlug returns an error from the named slug instead of an
	// output. Order rules same as outputsBySlug.
	errBySlug map[string][]error
}

func newMockRunner() *mockRunner {
	return &mockRunner{
		outputsBySlug: map[string][]string{},
		errBySlug:     map[string][]error{},
	}
}

func (m *mockRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	if errs := m.errBySlug[req.AgentSlug]; len(errs) > 0 {
		err := errs[0]
		m.errBySlug[req.AgentSlug] = errs[1:]
		if err != nil {
			return AgentStepResult{}, err
		}
	}
	outs := m.outputsBySlug[req.AgentSlug]
	if len(outs) == 0 {
		return AgentStepResult{Output: "default-output-from-" + req.AgentSlug, CostUSD: 0.001}, nil
	}
	out := outs[0]
	m.outputsBySlug[req.AgentSlug] = outs[1:]
	return AgentStepResult{Output: out, CostUSD: 0.001, DurationMs: 10}, nil
}

// captureEmitter records every journal Entry the executor emits so
// tests can assert on the run/step lifecycle without spinning up a
// real journal Writer.
type captureEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (c *captureEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	return "id_" + string(e.Type), nil
}

func (c *captureEmitter) typesEmitted() []journal.EntryType {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]journal.EntryType, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.Type)
	}
	return out
}

// fakePipeline returns a saved-pipeline shaped struct with the given
// DSL pre-marshalled and the test_run gate already passing. Used by
// tests that exercise the Run (not RunDefinition) code path.
func fakePipeline(t *testing.T, slug, definitionJSON, authorCrew, authorAgent string) *Pipeline {
	t.Helper()
	now := time.Now()
	return &Pipeline{
		ID:                "pln_test_" + slug,
		WorkspaceID:       "ws_test",
		Slug:              slug,
		Name:              slug,
		DSLVersion:        "1.0",
		DefinitionJSON:    definitionJSON,
		DefinitionHash:    definitionHash(definitionJSON),
		WorkspaceVisible:  true,
		AuthorCrewID:      authorCrew,
		AuthorAgentID:     authorAgent,
		AuthoredVia:       AuthoredViaAgent,
		LastTestRunAt:     &now,
		LastTestRunPassed: true,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func openExecutorTestDB(t *testing.T) (*Store, *Resolver, func()) {
	t.Helper()
	db := openStoreTestDB(t)
	return NewStore(db), NewResolver(db), func() { _ = db.Close() }
}

func TestExecutor_RunDefinition_HappyPath(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"hello world"}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "say hi"},
		},
	}
	in := RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
	}
	res, err := exec.RunDefinition(context.Background(), dsl, in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q", res.Status)
	}
	if res.Output != "hello world" {
		t.Errorf("output: got %q", res.Output)
	}
	if got := em.typesEmitted(); !containsType(got, journal.EntryPipelineRunStarted) || !containsType(got, journal.EntryPipelineRunCompleted) {
		t.Errorf("expected run.started + run.completed in journal, got: %v", got)
	}
}

func TestExecutor_TemplateSubstitution(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["fetch_agent"] = []string{`["email-1","email-2"]`}
	runner.outputsBySlug["sum_agent"] = []string{"summary of 2 emails"}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Inputs: []InputSpec{
			{Name: "since", Type: "string", Default: "yesterday"},
		},
		Steps: []Step{
			{ID: "fetch", Type: StepAgentRun, AgentSlug: "fetch_agent",
				Prompt: "fetch since {{ inputs.since }}"},
			{ID: "summarize", Type: StepAgentRun, AgentSlug: "sum_agent",
				Prompt: "summarize: {{ steps.fetch.output }}"},
		},
	}
	in := RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Inputs:       map[string]any{"since": "2026-05-01"},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q", res.Status)
	}
	// First call should have rendered prompt with input substitution.
	if got := runner.calls[0].Prompt; got != "fetch since 2026-05-01" {
		t.Errorf("step 1 prompt: got %q", got)
	}
	// Second call should have rendered with previous step's output.
	if got := runner.calls[1].Prompt; got != `summarize: ["email-1","email-2"]` {
		t.Errorf("step 2 prompt: got %q", got)
	}
}

func TestExecutor_AuthorCrewContext_NotInvokerCrew(t *testing.T) {
	// Cross-crew reuse contract: when Crew B invokes Crew A's
	// pipeline, the agent_run step lands in Crew A's context.
	// runner.calls[i].AuthorCrewID must be Crew A's id, never B's.
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dslJSON := `{"name":"demo","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"x"}]}`
	pipe := fakePipeline(t, "demo", dslJSON, "crew_a", "agent_lead")
	// Inject the pipeline directly into store via SQL to avoid the
	// test-run gate.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.db.ExecContext(context.Background(), `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, workspace_visible, author_crew_id, author_agent_id, authored_via, last_test_run_at, last_test_run_passed, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, 'agent_tool_call', ?, 1, ?, ?)`,
		pipe.ID, pipe.WorkspaceID, pipe.Slug, pipe.Name, pipe.DefinitionJSON, pipe.DefinitionHash,
		pipe.AuthorCrewID, pipe.AuthorAgentID, now, now, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	in := RunInput{
		PipelineID:      pipe.ID,
		WorkspaceID:     "ws_test",
		InvokingCrewID:  "crew_b",
		InvokingAgentID: "agent_b_lead",
		Mode:            ModeRun,
	}
	if _, err := exec.Run(context.Background(), in); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
	}
	got := runner.calls[0]
	if got.AuthorCrewID != "crew_a" {
		t.Errorf("execution crew context should be Crew A, got %q", got.AuthorCrewID)
	}
	if got.InvokingCrewID != "crew_b" {
		t.Errorf("invoking crew should be Crew B, got %q", got.InvokingCrewID)
	}
}

func TestExecutor_DryRun_NoAgentInvocation(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "do x"},
			{ID: "b", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "do y after {{ steps.a.output }}"},
		},
	}
	in := RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeDryRun,
	}
	res, err := exec.RunDefinition(context.Background(), dsl, in)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Status != "DRY_RUN_OK" {
		t.Errorf("status: got %q", res.Status)
	}
	if len(runner.calls) != 0 {
		t.Errorf("dry run should NOT invoke runner; got %d calls", len(runner.calls))
	}
	if len(res.WouldExecute) != 2 {
		t.Errorf("would_execute: got %d, want 2", len(res.WouldExecute))
	}
	if res.WouldExecute[0].WouldCallAgent != "agent_lead" {
		t.Errorf("step 0 would_call_agent: got %q", res.WouldExecute[0].WouldCallAgent)
	}
	if res.WouldExecute[0].WouldPass != "do x" {
		t.Errorf("step 0 would_pass: got %q", res.WouldExecute[0].WouldPass)
	}
}

func TestExecutor_ValidationGate_AbortsOnFail(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"x"} // shorter than min_length=10
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	min := 10
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Validation: &Validation{MinLength: &min},
				OnFail:     OnFailAbort,
			},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("status: got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "min") {
		t.Errorf("error should mention min length: %q", res.ErrorMessage)
	}
	types := em.typesEmitted()
	if !containsType(types, journal.EntryPipelineStepValidation) {
		t.Errorf("expected validation_failed entry, got: %v", types)
	}
	if !containsType(types, journal.EntryPipelineRunFailed) {
		t.Errorf("expected run.failed entry, got: %v", types)
	}
}

func TestExecutor_ValidationGate_EscalatesTier(t *testing.T) {
	// Step has on_fail=escalate_tier, output on first try fails
	// validation, but second try (next tier) passes. The runner
	// receives 2 calls (primary tier + fallback) and the run
	// completes successfully.
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"bad", "this is good output"}
	em := &captureEmitter{}
	// Custom workspace that maps fast → claude/haiku with sonnet fallback.
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE workspaces SET execution_tiers_json = ? WHERE id = ?`,
		`{"fast":{"primary":{"adapter":"claude","model":"haiku"},"fallback":[{"adapter":"claude","model":"sonnet"}]}}`,
		"ws_test"); err != nil {
		t.Fatalf("update tiers: %v", err)
	}
	exec := NewExecutor(store, resolver, runner, em)

	min := 10
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Complexity: ComplexityFast,
				Validation: &Validation{MinLength: &min},
				OnFail:     OnFailEscalateTier,
			},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q (err=%q)", res.Status, res.ErrorMessage)
	}
	if len(runner.calls) != 2 {
		t.Errorf("expected 2 runner calls (escalation), got %d", len(runner.calls))
	}
	if runner.calls[0].Model != "haiku" {
		t.Errorf("first attempt model: got %q", runner.calls[0].Model)
	}
	if runner.calls[1].Model != "sonnet" {
		t.Errorf("escalated model: got %q", runner.calls[1].Model)
	}
	if !containsType(em.typesEmitted(), journal.EntryPipelineStepValidation) {
		t.Errorf("expected validation_failed journal entry on first failure")
	}
}

func TestExecutor_MustNotContain_Banned(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"sure, here is API_KEY=sk_xyz"}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Validation: &Validation{MustNotContain: []string{"API_KEY="}},
				OnFail:     OnFailAbort,
			},
		},
	}
	res, runErr := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if runErr != nil {
		t.Fatalf("unexpected executor error: %v", runErr)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED for banned-token output, got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "API_KEY") {
		t.Errorf("error message should mention banned token: %q", res.ErrorMessage)
	}
}

func TestExecutor_RunnerError_SurfacesAsStepFailure(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["agent_lead"] = []error{errors.New("simulated network failure")}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x", OnFail: OnFailAbort},
		},
	}
	res, runErr := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if runErr != nil {
		t.Fatalf("unexpected executor error: %v", runErr)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED, got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "simulated network failure") {
		t.Errorf("error should propagate from runner: %q", res.ErrorMessage)
	}
}

// pipeResolverFn lets a test inject a closure as a PipelineResolver.
type pipeResolverFn func(ctx context.Context, ws, slug string) (*Pipeline, error)

func (f pipeResolverFn) GetBySlug(ctx context.Context, ws, slug string) (*Pipeline, error) {
	return f(ctx, ws, slug)
}

func TestExecutor_CallPipeline_Composition(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"nested-output"}
	em := &captureEmitter{}

	innerJSON := `{"name":"inner","steps":[{"id":"x","type":"agent_run","agent_slug":"agent_lead","prompt":"do x"}]}`
	innerPipe := fakePipeline(t, "inner", innerJSON, "crew_a", "agent_lead")

	pipes := pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "inner" {
			return innerPipe, nil
		}
		return nil, ErrNotFound
	})
	exec := NewExecutor(store, resolver, runner, em).WithPipelineResolver(pipes)

	outer := &DSL{
		Name: "outer",
		Steps: []Step{
			{ID: "call_inner", Type: StepCallPipeline, PipelineSlug: "inner"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), outer, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q (err=%q)", res.Status, res.ErrorMessage)
	}
	if res.Output != "nested-output" {
		t.Errorf("nested output should bubble up, got %q", res.Output)
	}
	if len(runner.calls) != 1 {
		t.Errorf("expected 1 runner call inside nested pipeline, got %d", len(runner.calls))
	}
}

func TestExecutor_CallPipeline_TargetNotFound(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	pipes := pipeResolverFn(func(_ context.Context, _, _ string) (*Pipeline, error) {
		return nil, ErrNotFound
	})
	exec := NewExecutor(store, resolver, runner, em).WithPipelineResolver(pipes)

	dsl := &DSL{
		Name: "outer",
		Steps: []Step{
			{ID: "call_ghost", Type: StepCallPipeline, PipelineSlug: "ghost"},
		},
	}
	res, runErr := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if runErr != nil {
		t.Fatalf("unexpected executor error: %v", runErr)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED for unknown call_pipeline target, got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "not found") {
		t.Errorf("error should mention not found: %q", res.ErrorMessage)
	}
}

func containsType(types []journal.EntryType, want journal.EntryType) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}
