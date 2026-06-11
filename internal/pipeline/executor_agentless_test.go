package pipeline

import (
	"context"
	"strings"
	"testing"
)

// The save-time validator is the front door for the agentless
// guarantee; this pins the runtime belt-and-braces. A definition that
// reaches the executor with agentless=true AND an agent_run step
// (row written before the validator existed, or smuggled past it)
// must fail the run WITHOUT the runner ever being invoked.
func TestExecutor_Agentless_RuntimeGuardBlocksAgentRun(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "smuggled",
		Agentless:  true,
		Steps: []Step{
			{ID: "shape", Type: StepTransform, Transform: &TransformStep{Input: "x", Expression: "."}},
			{ID: "think", Type: StepAgentRun, AgentSlug: "analyst", Prompt: "summarize"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
	})
	if err != nil {
		t.Fatalf("run returned transport error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status: got %q, want FAILED", res.Status)
	}
	if res.FailedAtStep != "think" {
		t.Errorf("failed_at_step: got %q, want %q", res.FailedAtStep, "think")
	}
	if !strings.Contains(res.ErrorMessage, "agentless") {
		t.Errorf("error message should name the agentless guarantee, got: %q", res.ErrorMessage)
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner must never be invoked for an agentless run with an LLM step; got %d calls", len(runner.calls))
	}
}

// An honest agentless routine (transform only) runs to completion —
// the guard must not interfere with the allowed step kinds.
func TestExecutor_Agentless_AllowsNonLLMSteps(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Agentless:  true,
		Steps: []Step{
			{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: "true", Expression: "."}},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: got %q (error: %s)", res.Status, res.ErrorMessage)
	}
	if res.Output != "true" {
		t.Errorf("output: got %q, want %q", res.Output, "true")
	}
	if res.CostUSD != 0 {
		t.Errorf("agentless run must cost $0, got %f", res.CostUSD)
	}
}
