package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// Security (go/allocation-size-overflow, #2456): three allocations in this
// package are sized from routine-supplied counts — a script step's args and
// env, a code step's env, and the per-item inputs copy inside a foreach.
// Routines can be agent-authored and run inputs are caller-supplied
// (mergeInputs keeps undeclared extras), so each count needs a declared
// maximum enforced before the allocation. These tests feed one-over-the-cap
// declarations and expect a rejection naming the field and the maximum, plus
// an in-range case that still runs.

// manyInputs builds n distinct run inputs.
func manyInputs(n int) map[string]any {
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		m["in"+strconv.Itoa(i)] = i
	}
	return m
}

// manyEnv builds n distinct env declarations.
func manyEnv(n int) map[string]string {
	m := make(map[string]string, n)
	for i := 0; i < n; i++ {
		m["E"+strconv.Itoa(i)] = "v"
	}
	return m
}

func TestScriptStep_BoundsArgsAndEnv(t *testing.T) {
	cases := []struct {
		name    string
		args    int
		inputs  int
		env     int
		wantErr string // empty = must succeed
	}{
		{name: "in-range args and env", args: maxScriptArgs, inputs: 4, env: maxStepEnvEntries - 4},
		{name: "one arg over", args: maxScriptArgs + 1, wantErr: "script.args"},
		{name: "inputs alone over", inputs: maxStepEnvEntries + 1, wantErr: "env entries"},
		{name: "inputs plus env over", inputs: maxStepEnvEntries / 2, env: maxStepEnvEntries/2 + 1, wantErr: "env entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			fake := &fakeScriptRunner{result: ScriptRunResult{Stdout: "ok", ExitCode: 0}}
			exec := NewExecutor(store, resolver, nil, nil).WithScriptRunner(fake)

			args := make([]string, c.args)
			for i := range args {
				args[i] = "a"
			}
			step := Step{
				ID:     "s",
				Type:   StepScript,
				Script: &ScriptStep{Path: "scripts/run.sh", Args: args, Env: manyEnv(c.env)},
			}
			rc := RenderContext{Inputs: manyInputs(c.inputs)}
			out, _, _, err := exec.runScriptStep(context.Background(), step, rc, RunInput{WorkspaceID: "ws_1"}, "run_1")
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("in-range step failed: %v", err)
				}
				if out != "ok" {
					t.Fatalf("out = %q, want ok", out)
				}
				if got := len(fake.last.Env); got != c.inputs+c.env {
					t.Errorf("env entries = %d, want %d", got, c.inputs+c.env)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection mentioning %q, got nil (runner called: %v)", c.wantErr, fake.last.Path != "")
			}
			if fake.last.Path != "" {
				t.Errorf("runner must not be called for an over-cap step")
			}
			assertBoundError(t, err, c.wantErr)
		})
	}
}

func TestCodeStep_BoundsEnv(t *testing.T) {
	cases := []struct {
		name    string
		inputs  int
		env     int
		wantErr string
	}{
		{name: "in-range", inputs: 8, env: maxStepEnvEntries - 8},
		{name: "inputs alone over", inputs: maxStepEnvEntries + 1, wantErr: "env entries"},
		{name: "inputs plus env over", inputs: maxStepEnvEntries / 2, env: maxStepEnvEntries/2 + 1, wantErr: "env entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			fake := &fakeCodeRunner{result: CodeRunResult{Stdout: "ok", ExitCode: 0}}
			exec := NewExecutor(store, resolver, nil, nil).WithCodeRunner(fake)

			step := Step{
				ID:   "c",
				Type: StepCode,
				Code: &CodeStep{Runtime: "python", Code: "print('x')", Env: manyEnv(c.env)},
			}
			rc := RenderContext{Inputs: manyInputs(c.inputs)}
			out, _, _, err := exec.runCodeStep(context.Background(), step, rc, RunInput{WorkspaceID: "ws_1"})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("in-range step failed: %v", err)
				}
				if out != "ok" {
					t.Fatalf("out = %q, want ok", out)
				}
				if got := len(fake.last.InputEnv); got != c.inputs+c.env {
					t.Errorf("env entries = %d, want %d", got, c.inputs+c.env)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection mentioning %q, got nil", c.wantErr)
			}
			if fake.last.Runtime != "" {
				t.Errorf("runner must not be called for an over-cap step")
			}
			assertBoundError(t, err, c.wantErr)
		})
	}
}

func TestExecutor_Foreach_BoundsItemInputs(t *testing.T) {
	cases := []struct {
		name    string
		inputs  int // run inputs besides the items array
		wantErr string
	}{
		{name: "in-range", inputs: maxForeachItemInputs - 1},
		{name: "one over", inputs: maxForeachItemInputs, wantErr: "inputs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			runner := &echoRunner{}
			exec := NewExecutor(store, resolver, runner, nil)

			inputs := manyInputs(c.inputs)
			inputs["arr"] = []any{"a", "b"}
			dsl := &DSL{
				Name: "bounded",
				Steps: []Step{{
					ID:   "each",
					Type: StepForeach,
					Foreach: &ForeachStep{
						Items: `{{ inputs.arr }}`,
						As:    "item",
						Steps: []Step{{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "{{ inputs.item }}"}},
					},
				}},
			}
			res, _ := exec.RunDefinition(context.Background(), dsl, RunInput{
				WorkspaceID:  "ws_test",
				AuthorCrewID: "crew_a",
				Mode:         ModeRun,
				Inputs:       inputs,
			})
			if c.wantErr == "" {
				if res.Status != "COMPLETED" {
					t.Fatalf("in-range foreach: status %s (%s)", res.Status, res.ErrorMessage)
				}
				if runner.calls() != 2 {
					t.Errorf("body ran %d times, want 2", runner.calls())
				}
				return
			}
			if res.Status == "COMPLETED" {
				t.Fatalf("expected the over-cap foreach to fail, got COMPLETED")
			}
			if runner.calls() != 0 {
				t.Errorf("body must not run for an over-cap inputs map, ran %d", runner.calls())
			}
			assertBoundError(t, fmt.Errorf("%s", res.ErrorMessage), c.wantErr)
			if !strings.Contains(res.ErrorMessage, strconv.Itoa(maxForeachItemInputs)) {
				t.Errorf("error must name the maximum %d: %q", maxForeachItemInputs, res.ErrorMessage)
			}
		})
	}
}

// assertBoundError checks the rejection names the field and the word
// "maximum" so an author can find the cap without reading the source.
func assertBoundError(t *testing.T, err error, field string) {
	t.Helper()
	msg := err.Error()
	if !strings.Contains(msg, field) {
		t.Errorf("error must name the field %q: %q", field, msg)
	}
	if !strings.Contains(msg, "maximum") {
		t.Errorf("error must state the maximum: %q", msg)
	}
}
