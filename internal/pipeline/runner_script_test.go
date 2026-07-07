package pipeline

import (
	"context"
	"strings"
	"testing"
)

// fakeScriptRunner captures the last request and returns a canned result,
// so the runner's contract (interpreter inference, path resolution, input
// env, arg rendering, exit-code handling) can be asserted without a real
// container.
type fakeScriptRunner struct {
	last   ScriptRunRequest
	result ScriptRunResult
	err    error
}

func (f *fakeScriptRunner) RunScript(_ context.Context, req ScriptRunRequest) (ScriptRunResult, error) {
	f.last = req
	return f.result, f.err
}

func TestScriptStep_RunsAndRendersInputs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: `{"ucet":"123","soucet_ok":true}`, ExitCode: 0}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)

	step := Step{
		ID:   "parse",
		Type: StepScript,
		Script: &ScriptStep{
			Path: "scripts/parse_vypis.py",
			Args: []string{"{{ inputs.file }}"},
			Env:  map[string]string{"MODE": "{{ inputs.mode }}"},
		},
	}
	rc := RenderContext{Inputs: map[string]any{"file": "vypis.pdf", "mode": "strict"}}
	out, _, _, err := exec.runScriptStep(context.Background(), step, rc, RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"}, "run_1")
	if err != nil {
		t.Fatalf("script step: %v", err)
	}
	if !strings.Contains(out, `"soucet_ok":true`) {
		t.Fatalf("stdout not returned as step output: %q", out)
	}
	if fake.last.Interpreter != "python3" {
		t.Errorf("interpreter = %q, want python3 (inferred from .py)", fake.last.Interpreter)
	}
	if fake.last.Path != "/crew/shared/scripts/parse_vypis.py" {
		t.Errorf("path = %q, want /crew/shared/scripts/parse_vypis.py", fake.last.Path)
	}
	if len(fake.last.Args) != 1 || fake.last.Args[0] != "vypis.pdf" {
		t.Errorf("args = %v, want [vypis.pdf]", fake.last.Args)
	}
	if fake.last.Env["CREWSHIP_INPUT_FILE"] != "vypis.pdf" {
		t.Errorf("CREWSHIP_INPUT_FILE = %q, want vypis.pdf", fake.last.Env["CREWSHIP_INPUT_FILE"])
	}
	if fake.last.Env["MODE"] != "strict" {
		t.Errorf("MODE env = %q, want strict (rendered)", fake.last.Env["MODE"])
	}
	if fake.last.WorkspaceID != "ws_1" || fake.last.AuthorCrewID != "crew_1" {
		t.Errorf("crew scope not threaded: ws=%q crew=%q", fake.last.WorkspaceID, fake.last.AuthorCrewID)
	}
}

func TestScriptStep_ExplicitInterpreterAndAbsolutePath(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "ok", ExitCode: 0}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)
	step := Step{ID: "run", Type: StepScript, Script: &ScriptStep{
		Path:        "/crew/shared/bin/reconcile",
		Interpreter: "bash",
	}}
	if _, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1"); err != nil {
		t.Fatalf("script step: %v", err)
	}
	if fake.last.Interpreter != "bash" {
		t.Errorf("interpreter = %q, want bash (explicit)", fake.last.Interpreter)
	}
	if fake.last.Path != "/crew/shared/bin/reconcile" {
		t.Errorf("path = %q, want /crew/shared/bin/reconcile", fake.last.Path)
	}
}

func TestScriptStep_NonZeroExitFails(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "partial", Stderr: "Traceback: boom", ExitCode: 1}}
	exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)
	step := Step{ID: "parse", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py"}}
	out, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1")
	if err == nil {
		t.Fatal("expected error on exit code 1")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should surface stderr, got: %v", err)
	}
	if out != "partial" {
		t.Errorf("partial stdout should still be returned, got %q", out)
	}
}

func TestScriptStep_NotWired(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	exec := NewExecutor(store, resolver, nil, nil) // no WithScriptRunner
	step := Step{ID: "parse", Type: StepScript, Script: &ScriptStep{Path: "scripts/x.py"}}
	_, _, _, err := exec.runScriptStep(context.Background(), step, RenderContext{}, RunInput{}, "run_1")
	if err == nil || !strings.Contains(err.Error(), "no ScriptRunner wired") {
		t.Fatalf("expected 'no ScriptRunner wired' error, got %v", err)
	}
}

func TestValidateScriptStep(t *testing.T) {
	cases := []struct {
		name string
		s    *ScriptStep
		ok   bool
	}{
		{"valid python by ext", &ScriptStep{Path: "scripts/parse.py"}, true},
		{"valid explicit interpreter", &ScriptStep{Path: "bin/run", Interpreter: "bash"}, true},
		{"valid absolute under shared", &ScriptStep{Path: "/crew/shared/scripts/x.py"}, true},
		{"missing body path", &ScriptStep{Path: ""}, false},
		{"traversal escapes shared", &ScriptStep{Path: "../../etc/passwd"}, false},
		{"absolute outside crew", &ScriptStep{Path: "/etc/passwd"}, false},
		{"unknown ext no interpreter", &ScriptStep{Path: "scripts/mystery"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStepEgress(Step{ID: "s", Type: StepScript, Script: tc.s})
			if tc.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error for %+v", tc.s)
			}
		})
	}
	// nil body
	if err := validateStepEgress(Step{ID: "s", Type: StepScript}); err == nil {
		t.Error("expected error for nil script body")
	}
}
