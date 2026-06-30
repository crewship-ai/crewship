package pipeline

import (
	"context"
	"encoding/json"
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

// @json on RAW (non-JSON) input must encode it as a JSON string literal —
// the safe way to build a JSON body from agent plain text, e.g.
// {"content": {{ steps.x | @json }}}. Previously this errored ("input is not
// JSON"), forcing fragile agent-emitted JSON (the smartmania→discord 400 bug).
func TestTransform_JSON_RawStringEncodes(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)
	step := Step{
		ID: "enc", Type: StepTransform,
		Transform: &TransformStep{Input: "say \"hi\"\nline2", Expression: "@json"},
	}
	out, _, _, err := exec.runTransformStep(step, RenderContext{})
	if err != nil {
		t.Fatalf("@json raw string: %v", err)
	}
	if out != `"say \"hi\"\nline2"` {
		t.Errorf("@json raw string = %s, want a quoted+escaped JSON string", out)
	}
	// And it must compose into a valid JSON object body.
	body := `{"content": ` + out + `}`
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Errorf("composed body is not valid JSON: %v (%s)", err, body)
	}
}

func TestTransform_CanonicalJSON(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)

	// Two inputs that differ only in whitespace + key order (the second
	// also wrapped in a ```json fence, as LLM output often is) must
	// collapse to the identical canonical byte string — this is what
	// makes a "recipe" routine's JSON output reproducible on Haiku.
	a := Step{ID: "a", Type: StepTransform, Transform: &TransformStep{
		Input:      `{"b": 2, "a": 1}`,
		Expression: "@json",
	}}
	b := Step{ID: "b", Type: StepTransform, Transform: &TransformStep{
		Input:      "```json\n{\n  \"a\": 1,\n  \"b\": 2\n}\n```",
		Expression: "@json",
	}}
	outA, _, _, err := exec.runTransformStep(a, RenderContext{})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	outB, _, _, err := exec.runTransformStep(b, RenderContext{})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	const want = `{"a":1,"b":2}`
	if outA != want {
		t.Errorf("a canonical = %q, want %q", outA, want)
	}
	if outA != outB {
		t.Errorf("canonicalisation not byte-stable: %q vs %q", outA, outB)
	}
}

func TestTransform_CanonicalJSON_PreservesLargeNumbers(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil)

	// A 20-digit integer and a high-precision decimal must survive @json
	// verbatim. A float64 decode would round these (e.g. the int becomes
	// 1.2345678901234568e+19), breaking byte-stability — the regression
	// UseNumber decoding guards against.
	s := Step{ID: "n", Type: StepTransform, Transform: &TransformStep{
		Input:      `{"big": 12345678901234567890, "frac": 0.12345678901234567}`,
		Expression: "@json",
	}}
	out, _, _, err := exec.runTransformStep(s, RenderContext{})
	if err != nil {
		t.Fatalf("runTransformStep: %v", err)
	}
	const want = `{"big":12345678901234567890,"frac":0.12345678901234567}`
	if out != want {
		t.Errorf("@json = %q, want %q (numbers must keep full precision)", out, want)
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
