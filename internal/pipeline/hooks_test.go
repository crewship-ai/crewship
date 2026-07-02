package pipeline

import (
	"context"
	"strings"
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

// TestRunHooksAround_AfterAllFailure_RecordsRunWarning guards the fix for
// the "invisible teardown hook failure" bug: after_all/on_failure hooks
// are best-effort (must never flip a COMPLETED run to FAILED), but a
// failure used to be dropped into slog.Warn ONLY — nothing landed on the
// run record, so an operator had no way to discover a leaked credential
// or an unclosed cost meter short of grepping server logs. The hook
// failure must now show up as a structured warning on the run.
func TestRunHooksAround_AfterAllFailure_RecordsRunWarning(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	runStore := NewRunStore(db)
	e := &Executor{codeRunner: NewMultiCodeRunner(), runStore: runStore}
	ctx := context.Background()

	runID := "run-warn-1"
	if err := runStore.Insert(ctx, &RunRecord{
		ID: runID, WorkspaceID: "ws_test", PipelineID: "pln_x", PipelineSlug: "x", Status: RunStatusRunning,
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}

	body := func() (*RunResult, error) {
		return &RunResult{RunID: runID, Status: "COMPLETED"}, nil
	}
	in := RunInput{PipelineID: "p", dsl: &DSL{Hooks: &RoutineHooks{
		// Unresolvable var -> runRoutineHook returns an error without
		// running body — exactly the "teardown step failed" shape.
		AfterAll: &Step{ID: "post", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "missing.var > 1"}},
	}}}

	res, err := e.runHooksAround(ctx, in, runID, "slug", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Best-effort contract: the failing teardown hook must NOT flip the
	// run's outcome.
	if res.Status != "COMPLETED" {
		t.Fatalf("after_all failure must not flip status, got %q", res.Status)
	}

	rec, err := runStore.Get(ctx, runID)
	if err != nil {
		t.Fatalf("run row: %v", err)
	}
	warnings := rec.Warnings()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %d, want 1: %+v", len(warnings), warnings)
	}
	if warnings[0].Stage != "hook after_all" {
		t.Errorf("stage = %q, want %q", warnings[0].Stage, "hook after_all")
	}
	if !strings.Contains(warnings[0].Message, "missing") {
		t.Errorf("message = %q, want it to mention the hook error", warnings[0].Message)
	}
	if warnings[0].At.IsZero() {
		t.Error("warning At timestamp not stamped")
	}
}

// TestRunHooksAround_OnFailureFailure_RecordsRunWarning mirrors the
// after_all case for on_failure: the run stays FAILED (its actual
// outcome), but the teardown hook's own failure is additionally
// recorded as a warning rather than only logged.
func TestRunHooksAround_OnFailureFailure_RecordsRunWarning(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	if _, err := db.Exec(runsProjectionDDL); err != nil {
		t.Fatalf("runs ddl: %v", err)
	}
	runStore := NewRunStore(db)
	e := &Executor{codeRunner: NewMultiCodeRunner(), runStore: runStore}
	ctx := context.Background()

	runID := "run-warn-2"
	if err := runStore.Insert(ctx, &RunRecord{
		ID: runID, WorkspaceID: "ws_test", PipelineID: "pln_x", PipelineSlug: "x", Status: RunStatusRunning,
	}); err != nil {
		t.Fatalf("seed run row: %v", err)
	}

	body := func() (*RunResult, error) {
		return &RunResult{RunID: runID, Status: "FAILED", ErrorMessage: "step boom"}, nil
	}
	in := RunInput{PipelineID: "p", dsl: &DSL{Hooks: &RoutineHooks{
		OnFailure: &Step{ID: "cleanup", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "missing.var > 1"}},
	}}}

	res, err := e.runHooksAround(ctx, in, runID, "slug", body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("status = %q, want FAILED (the body's own outcome)", res.Status)
	}

	rec, err := runStore.Get(ctx, runID)
	if err != nil {
		t.Fatalf("run row: %v", err)
	}
	warnings := rec.Warnings()
	if len(warnings) != 1 || warnings[0].Stage != "hook on_failure" {
		t.Fatalf("warnings = %+v, want one entry with stage %q", warnings, "hook on_failure")
	}
}
