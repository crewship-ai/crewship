package pipeline

import (
	"context"
	"testing"
)

func TestValidateHooks_RejectsNonSideChannelTypes(t *testing.T) {
	base := &DSL{Name: "h", Steps: []Step{{ID: "s", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}}}}

	// agent_run hook → rejected.
	base.Hooks = &RoutineHooks{BeforeAll: &Step{ID: "hk", Type: StepAgentRun, AgentSlug: "x", Prompt: "y"}}
	if err := validateHooks(base); err == nil {
		t.Fatal("agent_run hook should be rejected")
	}
	// code hook → allowed.
	base.Hooks = &RoutineHooks{AfterAll: &Step{ID: "hk", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}}}
	if err := validateHooks(base); err != nil {
		t.Fatalf("code hook should be allowed: %v", err)
	}
}

func TestValidateStepHooks(t *testing.T) {
	// agent_run per-step hook → rejected.
	st := Step{ID: "s", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"},
		Hooks: &StepHooks{Before: &Step{ID: "b", Type: StepAgentRun, AgentSlug: "x", Prompt: "y"}}}
	if err := validateStepHooks(st); err == nil {
		t.Fatal("agent_run step hook should be rejected")
	}
	// http after hook → allowed.
	st.Hooks = &StepHooks{After: &Step{ID: "a", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://x"}}}
	if err := validateStepHooks(st); err != nil {
		t.Fatalf("http step hook should be allowed: %v", err)
	}
}

func TestRunHooksAround(t *testing.T) {
	e := &Executor{codeRunner: NewMultiCodeRunner()}
	ctx := context.Background()
	bodyRan := false
	body := func() (*RunResult, error) {
		bodyRan = true
		return &RunResult{RunID: "r", Status: "COMPLETED"}, nil
	}

	// before_all that COMPILE-fails (unknown var) aborts: body must NOT run.
	in := RunInput{PipelineID: "p", dsl: &DSL{Hooks: &RoutineHooks{
		BeforeAll: &Step{ID: "pre", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "missing.var > 1"}},
	}}}
	res, err := e.runHooksAround(ctx, in, "run1", "slug", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if bodyRan {
		t.Fatal("body must not run when before_all fails")
	}
	if res.Status != "FAILED" || res.FailedAtStep != "pre" {
		t.Fatalf("before_all failure should yield FAILED at 'pre', got %s/%s", res.Status, res.FailedAtStep)
	}

	// Happy path: before_all passes, body runs, after_all best-effort.
	bodyRan = false
	in2 := RunInput{PipelineID: "p", dsl: &DSL{Hooks: &RoutineHooks{
		BeforeAll: &Step{ID: "pre", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "1 > 0"}},
		AfterAll:  &Step{ID: "post", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "2 > 1"}},
	}}}
	res, err = e.runHooksAround(ctx, in2, "run2", "slug", body)
	if err != nil || !bodyRan || res.Status != "COMPLETED" {
		t.Fatalf("happy path: bodyRan=%v status=%s err=%v", bodyRan, res.Status, err)
	}

	// before_all referencing a DEFAULTED input must see the default
	// (hooks run before runDSL merges defaults — merged in the hook too).
	bodyRan = false
	inDef := RunInput{PipelineID: "p", dsl: &DSL{
		Inputs: []InputSpec{{Name: "x", Type: "number", Default: 9.0}},
		Hooks:  &RoutineHooks{BeforeAll: &Step{ID: "pre", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "inputs.x > 0"}}},
	}}
	res, err = e.runHooksAround(ctx, inDef, "runD", "slug", body)
	if err != nil || !bodyRan || res.Status != "COMPLETED" {
		t.Fatalf("defaulted-input hook should pass: bodyRan=%v status=%s err=%v", bodyRan, res.Status, err)
	}

	// Resume re-entry skips hooks entirely (body runs, before_all ignored).
	bodyRan = false
	in3 := RunInput{PipelineID: "p", resume: true, dsl: &DSL{Hooks: &RoutineHooks{
		BeforeAll: &Step{ID: "pre", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "missing.var > 1"}},
	}}}
	_, err = e.runHooksAround(ctx, in3, "run3", "slug", body)
	if err != nil || !bodyRan {
		t.Fatalf("resume must skip hooks and run body: bodyRan=%v err=%v", bodyRan, err)
	}
}
