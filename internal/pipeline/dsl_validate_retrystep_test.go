package pipeline

import (
	"context"
	"errors"
	"testing"
)

// on_fail: retry_step is now sugar for the default retry policy (#861):
// a step declaring it — with no explicit retry: block — is retried on
// transient EXECUTION errors under defaultRetryPolicy. It must therefore
// pass validation, not be rejected. RED before #861: Validate returned an
// error naming retry_step.
func TestValidate_AcceptsRetryStep_StepLevel(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "r",
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go", OnFail: OnFailRetryStep},
		},
	}
	if err := Validate(d, map[string]struct{}{"worker": {}}, nil); err != nil {
		t.Fatalf("Validate rejected on_fail: retry_step: %v", err)
	}
}

func TestValidate_AcceptsRetryStep_OutcomesLevel(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "r",
		Steps: []Step{
			{
				ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
				Outcomes: &Outcomes{
					GraderAgentSlug: "worker",
					Criteria:        []OutcomeCriterion{{Name: "compiles", Rule: "the code compiles"}},
					OnFail:          OnFailRetryStep,
				},
			},
		},
	}
	if err := Validate(d, map[string]struct{}{"worker": {}}, nil); err != nil {
		t.Fatalf("Validate rejected outcomes.on_fail: retry_step: %v", err)
	}
}

// retry_step with no explicit retry: block desugars to defaultRetryPolicy
// at the chokepoint — the step is retried on a transient execution error.
func TestRetryStep_DesugarsToDefaultPolicy(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["worker"] = []error{
		errors.New("transient blip"),
		nil,
	}
	runner.outputsBySlug["worker"] = []string{"ok"}
	exec := NewExecutor(store, resolver, runner, nil)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", Steps: []Step{
		// No retry: block — on_fail: retry_step is the only opt-in.
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go", OnFail: OnFailRetryStep},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %s err=%s (retry_step should have retried the blip)", res.Status, res.ErrorMessage)
	}
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected 2 calls (1 blip + 1 success) from the default policy, got %d", got)
	}
}

// The supported values must still pass — the change must be surgical.
func TestValidate_AcceptsSupportedOnFail(t *testing.T) {
	for _, v := range []OnFailAction{"", OnFailEscalateTier, OnFailAbort, OnFailRetryStep} {
		d := &DSL{
			DSLVersion: "1.0",
			Name:       "r",
			Steps: []Step{
				{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go", OnFail: v},
			},
		}
		if err := Validate(d, map[string]struct{}{"worker": {}}, nil); err != nil {
			t.Errorf("on_fail=%q rejected: %v", v, err)
		}
	}
}
