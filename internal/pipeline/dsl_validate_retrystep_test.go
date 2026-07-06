package pipeline

import (
	"strings"
	"testing"
)

// on_fail: retry_step was ACCEPTED at validation but the executor never
// implemented a per-step retry budget — it degraded to abort, so the DSL did
// the opposite of what it promised. Until a real budget lands, reject it at
// authoring time (loud, at save/test-run) instead of silently misbehaving at
// 3am. RED on main: Validate accepts retry_step and returns nil.
func TestValidate_RejectsRetryStep_StepLevel(t *testing.T) {
	d := &DSL{
		DSLVersion: "1.0",
		Name:       "r",
		Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go", OnFail: OnFailRetryStep},
		},
	}
	err := Validate(d, map[string]struct{}{"worker": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "retry_step") {
		t.Fatalf("Validate = %v, want an error naming retry_step", err)
	}
}

func TestValidate_RejectsRetryStep_OutcomesLevel(t *testing.T) {
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
	err := Validate(d, map[string]struct{}{"worker": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "retry_step") {
		t.Fatalf("Validate = %v, want an error naming retry_step", err)
	}
}

// The supported values must still pass — the reject must be surgical.
func TestValidate_AcceptsSupportedOnFail(t *testing.T) {
	for _, v := range []OnFailAction{"", OnFailEscalateTier, OnFailAbort} {
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
