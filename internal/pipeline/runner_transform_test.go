package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestTransform_Identity(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{Input: "hello world", Expression: "."},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "hello world" {
		t.Errorf("identity: got %q", out)
	}
}

func TestTransform_FieldAccess(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{
			Input:      `{"user":{"name":"alice","age":30}}`,
			Expression: ".user.name",
		},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "alice" {
		t.Errorf("got %q", out)
	}
}

func TestTransform_ArrayIndex(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{
			Input:      `{"items":[{"id":"a"},{"id":"b"}]}`,
			Expression: ".items[1].id",
		},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "b" {
		t.Errorf("got %q", out)
	}
}

func TestTransform_Length(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{Input: `[1,2,3,4]`, Expression: "length"},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "4" {
		t.Errorf("got %q", out)
	}
}

func TestTransform_Keys(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{Input: `{"b":1,"a":2,"c":3}`, Expression: "keys"},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if !strings.Contains(out, `"a","b","c"`) {
		t.Errorf("keys should be sorted: got %q", out)
	}
}

func TestTransform_FieldNotFound_Errors(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{Input: `{"a":1}`, Expression: ".missing"},
	}
	_, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected field-not-found error, got %v", err)
	}
}

func TestTransform_TemplateSubstitutionInInput(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "t", Type: StepTransform,
		Transform: &TransformStep{
			Input:      "{{ steps.fetch.output }}",
			Expression: ".count",
		},
	}
	rctx := RenderContext{
		StepOutputs: map[string]string{"fetch": `{"count":42}`},
	}
	out, _, _, err := exec.runTransformStep(step, rctx)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	if out != "42" {
		t.Errorf("got %q", out)
	}
}

// stubCodeRunner satisfies CodeRunner for tests; returns canned
// stdout based on the script's first line.
type stubCodeRunner struct {
	output   string
	exitCode int
	err      error
}

func (s stubCodeRunner) RunCode(_ context.Context, _ CodeRunRequest) (CodeRunResult, error) {
	return CodeRunResult{Stdout: s.output, ExitCode: s.exitCode}, s.err
}

func TestCodeStep_NoRunnerFailsCleanly(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "c", Type: StepCode,
		Code: &CodeStep{Runtime: "python", Code: "print('hi')"},
	}
	_, _, _, err := exec.runCodeStep(context.Background(), step, RenderContext{}, RunInput{WorkspaceID: "ws_test"})
	if err == nil || !strings.Contains(err.Error(), "no CodeRunner wired") {
		t.Errorf("expected no-runner error, got %v", err)
	}
}

func TestCodeStep_RunnerStdoutFlowsAsOutput(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil).WithCodeRunner(stubCodeRunner{output: "hello\n"})
	step := Step{
		ID: "c", Type: StepCode,
		Code: &CodeStep{Runtime: "bash", Code: "echo hi"},
	}
	out, _, _, err := exec.runCodeStep(context.Background(), step, RenderContext{}, RunInput{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("code step: %v", err)
	}
	if out != "hello\n" {
		t.Errorf("output: got %q", out)
	}
}

func TestCodeStep_ExitNonZeroFails(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil).WithCodeRunner(stubCodeRunner{exitCode: 1})
	step := Step{
		ID: "c", Type: StepCode,
		Code: &CodeStep{Runtime: "bash", Code: "false"},
	}
	_, _, _, err := exec.runCodeStep(context.Background(), step, RenderContext{}, RunInput{WorkspaceID: "ws_test"})
	if err == nil || !strings.Contains(err.Error(), "exit code 1") {
		t.Errorf("expected exit-code error, got %v", err)
	}
}

func TestCodeStep_InputsExposedAsEnv(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	captured := stubCodeRunnerCapturing{}
	exec := NewExecutor(store, resolver, nil, nil).WithCodeRunner(&captured)
	step := Step{
		ID: "c", Type: StepCode,
		Code: &CodeStep{Runtime: "python", Code: "print('hi')"},
	}
	rctx := RenderContext{Inputs: map[string]any{"user": "pavel", "limit": 10}}
	if _, _, _, err := exec.runCodeStep(context.Background(), step, rctx, RunInput{WorkspaceID: "ws_test"}); err != nil {
		t.Fatalf("code step: %v", err)
	}
	if got := captured.lastReq.InputEnv["CREWSHIP_INPUT_USER"]; got != "pavel" {
		t.Errorf("CREWSHIP_INPUT_USER: got %q", got)
	}
	if got := captured.lastReq.InputEnv["CREWSHIP_INPUT_LIMIT"]; got != "10" {
		t.Errorf("CREWSHIP_INPUT_LIMIT: got %q", got)
	}
}

type stubCodeRunnerCapturing struct {
	lastReq CodeRunRequest
}

func (s *stubCodeRunnerCapturing) RunCode(_ context.Context, req CodeRunRequest) (CodeRunResult, error) {
	s.lastReq = req
	return CodeRunResult{Stdout: "ok", ExitCode: 0}, nil
}
