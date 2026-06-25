package pipeline

import (
	"context"
	"strings"
	"testing"
)

// The core unit: the expr runtime evaluates a single comparison and emits
// true/false. This is the wake-gate primitive behind agentless probes.
func TestExprCodeRunner_ComparisonEmitsBool(t *testing.T) {
	cases := []struct {
		code string
		env  map[string]string
		want string
	}{
		{code: "6 > 5", want: "true"},
		{code: "3 > 5", want: "false"},
		{code: "5 >= 5", want: "true"},
		{code: "4 <= 3", want: "false"},
		{code: "2 == 2", want: "true"},
		{code: "2 != 2", want: "false"},
		{code: "1.5 > 0.5", want: "true"},
		{code: `"a" == "a"`, want: "true"},
		{code: `"a" != "b"`, want: "true"},
		// CREWSHIP_INPUT_* operands resolve from env (the non-rendered path).
		{code: "CREWSHIP_INPUT_SPEND > CREWSHIP_INPUT_THRESHOLD",
			env:  map[string]string{"CREWSHIP_INPUT_SPEND": "9", "CREWSHIP_INPUT_THRESHOLD": "5"},
			want: "true"},
	}
	for _, c := range cases {
		res, err := ExprCodeRunner{}.RunCode(context.Background(), CodeRunRequest{
			Runtime: "expr", Code: c.code, InputEnv: c.env, TimeoutSec: 15, MaxBytes: 1_000_000,
		})
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.code, err)
			continue
		}
		if res.ExitCode != 0 {
			t.Errorf("%q: exit code %d, want 0", c.code, res.ExitCode)
		}
		if res.Stdout != c.want {
			t.Errorf("%q: stdout = %q, want %q", c.code, res.Stdout, c.want)
		}
	}
}

// Fail-closed: any runtime other than expr is rejected with guidance, never
// silently succeeds. This preserves the no-arbitrary-code-exec posture.
func TestExprCodeRunner_RejectsNonExprRuntime(t *testing.T) {
	for _, rt := range []string{"bash", "python", "go", ""} {
		_, err := ExprCodeRunner{}.RunCode(context.Background(), CodeRunRequest{
			Runtime: rt, Code: "echo hi", TimeoutSec: 15, MaxBytes: 1_000_000,
		})
		if err == nil {
			t.Errorf("runtime %q: expected an error, got nil", rt)
			continue
		}
		if !strings.Contains(err.Error(), "expr") || !strings.Contains(err.Error(), "agent_run") {
			t.Errorf("runtime %q: error should point at expr/agent_run, got: %v", rt, err)
		}
	}
}

// Malformed expressions fail closed rather than guessing.
func TestExprCodeRunner_MalformedFailsClosed(t *testing.T) {
	for _, code := range []string{"", "true", "5", "5 5"} {
		res, err := ExprCodeRunner{}.RunCode(context.Background(), CodeRunRequest{
			Runtime: "expr", Code: code, TimeoutSec: 15, MaxBytes: 1_000_000,
		})
		if err == nil {
			t.Errorf("%q: expected error, got stdout %q", code, res.Stdout)
		}
	}
}

// End-to-end through the executor: an agentless code probe runs to COMPLETED,
// emits its boolean, and NEVER invokes the agent runner (token-zero proof).
func TestExecutor_AgentlessCodeProbe_RunsTokenZero(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em).WithCodeRunner(ExprCodeRunner{})

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Agentless:  true,
		Steps: []Step{
			{ID: "probe", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "6 > 5"}},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
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
	if len(runner.calls) != 0 {
		t.Errorf("token-zero violated: agent runner called %d times", len(runner.calls))
	}
}

// The render fix: the code body must be rendered (inputs substituted) before
// dispatch, so {{ inputs.x }} works exactly like agent_run prompts.
func TestExecutor_CodeProbe_RendersInputs(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em).WithCodeRunner(ExprCodeRunner{})

	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "probe",
		Agentless:  true,
		Steps: []Step{
			{ID: "probe", Type: StepCode, Code: &CodeStep{
				Runtime: "expr", Code: "{{ inputs.spend_usd }} > {{ inputs.threshold_usd }}"}},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		Inputs:       map[string]any{"spend_usd": 6.0, "threshold_usd": 5.0},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: got %q (error: %s)", res.Status, res.ErrorMessage)
	}
	if res.Output != "true" {
		t.Errorf("rendered probe output: got %q, want %q (render fix not applied?)", res.Output, "true")
	}
}
