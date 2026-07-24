package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// #1417 — the governance status gate must be enforced inside runDSL, not
// only in Run(). A nested call_pipeline target that is `disabled`/`proposed`
// must be refused rather than executed.
func TestExecutor_StatusGate_CallPipeline(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["child-agent"] = []string{"child-output"}
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	// A disabled nested target — its DSL is perfectly valid and would run
	// to completion if the gate were bypassed.
	disabled := fakePipeline(t, "disabled-child",
		`{"dsl_version":"1.0","name":"disabled-child","steps":[{"id":"c1","type":"agent_run","agent_slug":"child-agent","prompt":"hi"}]}`,
		"crew_a", "agent_lead")
	disabled.Status = "disabled"
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "disabled-child" {
			return disabled, nil
		}
		return nil, errors.New("unexpected slug")
	}))

	parent := RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun}
	render := RenderContext{}

	// Belt: the explicit reject in runCallPipelineStep wraps ErrRoutineNotActive.
	_, _, _, err := exec.runCallPipelineStep(ctx,
		Step{ID: "s", Type: StepCallPipeline, PipelineSlug: "disabled-child"}, parent, render, 0, "run_parent", 0)
	if err == nil {
		t.Fatalf("expected disabled call_pipeline target to be rejected, got nil error")
	}
	if !errors.Is(err, ErrRoutineNotActive) {
		t.Errorf("expected ErrRoutineNotActive, got %v", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("disabled target must not invoke its agent; got %d calls", len(runner.calls))
	}
}

// Suspenders: even if a caller reaches runDSL directly with a non-runnable
// pipeline (bypassing runCallPipelineStep), the runtime gate stops it.
func TestExecutor_StatusGate_RunDSL(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["child-agent"] = []string{"child-output"}
	exec := NewExecutor(store, resolver, runner, nil)

	disabled := fakePipeline(t, "disabled-direct",
		`{"dsl_version":"1.0","name":"disabled-direct","steps":[{"id":"c1","type":"agent_run","agent_slug":"child-agent","prompt":"hi"}]}`,
		"crew_a", "agent_lead")
	disabled.Status = "disabled"
	dsl, perr := Parse([]byte(disabled.DefinitionJSON))
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}

	in := RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		pipeline:     disabled,
		dsl:          dsl,
	}
	// depth 1 == nested invocation (no persistence / span machinery).
	res, err := exec.runDSL(context.Background(), in, 1)
	if err != nil {
		t.Fatalf("runDSL returned hard error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED for disabled routine, got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, ErrRoutineNotActive.Error()) {
		t.Errorf("expected error message to mention %q, got %q", ErrRoutineNotActive.Error(), res.ErrorMessage)
	}
	if len(runner.calls) != 0 {
		t.Errorf("disabled routine must not invoke its agent; got %d calls", len(runner.calls))
	}
}
