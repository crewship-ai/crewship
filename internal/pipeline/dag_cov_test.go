package pipeline

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dag.go — runDAG's runtime validation failure, call_pipeline ban,
// cost-cap trip, RecordInvocation on DAG failure, and the final-output
// fallback when every leaf was skipped.
// ---------------------------------------------------------------------------

func TestRunDAG_RuntimeValidationFailure(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	dsl := &DSL{Name: "dag-bad", Steps: []Step{
		{ID: "b", Type: StepAgentRun, AgentSlug: "a", Prompt: "p", Needs: []string{"ghost"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "unknown step") {
		t.Errorf("validation failure: %+v", res)
	}
	if res.FailedAtStep != "b" {
		t.Errorf("FailedAtStep: %q", res.FailedAtStep)
	}
}

func TestRunDAG_CallPipelineForbidden(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, newMockRunner(), nil)

	dsl := &DSL{Name: "dag-call", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "p"},
		{ID: "b", Type: StepCallPipeline, PipelineSlug: "child", Needs: []string{"a"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "call_pipeline cannot be used inside a DAG") {
		t.Errorf("call_pipeline ban: %+v", res)
	}
	if res.FailedAtStep != "b" {
		t.Errorf("FailedAtStep: %q", res.FailedAtStep)
	}
}

// TestRunDAG_CostCapTrips covers executeOneStep's post-step cost gate
// AND runDAG's failure handling — including RecordInvocation(FAILED)
// on the saved-pipeline Run path.
func TestRunDAG_CostCapTrips(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner() // every step costs 0.001
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	in := validSaveInput("dag-capped")
	in.DefinitionJSON = `{"dsl_version":"1.0","name":"dag-capped","max_cost_usd":0.0005,"steps":[
		{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"p1"},
		{"id":"b","type":"agent_run","agent_slug":"agent_lead","prompt":"p2","needs":["a"]}
	]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	res, err := exec.Run(ctx, RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || !strings.Contains(res.ErrorMessage, "cost cap exceeded") {
		t.Errorf("cost cap: %+v", res)
	}
	if res.FailedAtStep != "a" {
		t.Errorf("FailedAtStep: %q", res.FailedAtStep)
	}
	// Step b must never have run — fail-fast cancelled the next wave.
	if len(runner.calls) != 1 {
		t.Errorf("expected only step a to run, got %d calls", len(runner.calls))
	}
	// And the invocation record reflects the failure.
	got, _ := store.GetByID(ctx, p.ID)
	if got.LastInvocationStatus != "FAILED" {
		t.Errorf("invocation status: %q", got.LastInvocationStatus)
	}
}

// TestRunDAG_StepErrorFailsRun covers the executeOneStep error branch
// (runner failure → firstErr + dagCancel) and the wave-boundary
// failure return.
func TestRunDAG_StepErrorFailsRun(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["agent_lead"] = []error{contextFreeErr("hard dag failure")}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "dag-err", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "p1"},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "p2", Needs: []string{"a"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" || res.FailedAtStep != "a" {
		t.Errorf("dag step error: %+v", res)
	}
	if !strings.Contains(res.ErrorMessage, "hard dag failure") {
		t.Errorf("error message: %q", res.ErrorMessage)
	}
}

// contextFreeErr builds an error whose text avoids every transient
// marker so the same-tier retry loop doesn't kick in (and sleep).
type contextFreeErr string

func (e contextFreeErr) Error() string { return string(e) }

// TestRunDAG_OutputFallback_WhenLeafSkipped pins the final-output
// selection: when the only leaf was skipped (if=false), the fallback
// walks back in source order to the last non-skipped output.
func TestRunDAG_OutputFallback_WhenLeafSkipped(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"root-output"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "dag-skip-leaf", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "p1"},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "p2",
			Needs: []string{"a"}, If: "{{ inputs.never }}"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %q (%s)", res.Status, res.ErrorMessage)
	}
	if res.StepOutputs["b"] != "<skipped>" {
		t.Errorf("leaf should be skipped, got %q", res.StepOutputs["b"])
	}
	if res.Output != "root-output" {
		t.Errorf("fallback output: %q, want root-output", res.Output)
	}
}
