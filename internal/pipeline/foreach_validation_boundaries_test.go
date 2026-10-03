package pipeline

import (
	"strings"
	"testing"
)

func foreachBoundaryDSL() *DSL {
	return &DSL{Name: "loop-check", Steps: []Step{{ID: "loop", Type: StepForeach, Foreach: &ForeachStep{Items: `["one"]`, Steps: []Step{{ID: "body", Type: StepTransform, Transform: &TransformStep{Input: "text", Expression: "."}}}}}}}
}

func TestForeachBodyCannotBypassStepPolicyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, part string
		change     func(*Step)
	}{
		{"complexity", "complexity", func(s *Step) { s.Complexity = "unbounded" }},
		{"failure policy", "on_fail", func(s *Step) { s.OnFail = "ignore-and-publish" }},
		{"retry bounds", "retry.max_attempts", func(s *Step) { s.Retry = &RetryPolicy{MaxAttempts: 0} }},
		{"contradictory output", "must_contain", func(s *Step) {
			s.Validation = &Validation{MustContain: []string{"token"}, MustNotContain: []string{"token"}}
		}},
		{"non-agent rubric", "only supported", func(s *Step) {
			s.Outcomes = &Outcomes{GraderAgentSlug: "grader", Criteria: []OutcomeCriterion{{Name: "ok", Rule: "valid"}}}
		}},
		{"recursive hook", "hook", func(s *Step) { s.Hooks = &StepHooks{Before: &Step{ID: "hook", Type: StepCallPipeline}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := foreachBoundaryDSL()
			tc.change(&d.Steps[0].Foreach.Steps[0])
			err := Validate(d, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("invalid foreach body escaped policy validation: %v", err)
			}
		})
	}
}

func TestForeachAuthoringValidatesCompleteBodyShape(t *testing.T) {
	if err := Validate(foreachBoundaryDSL(), nil, nil); err != nil {
		t.Fatalf("valid loop rejected: %v", err)
	}
	for _, tc := range []struct {
		name, part string
		change     func(*Step)
	}{
		{"missing block", "missing", func(s *Step) { s.Foreach = nil }},
		{"blank items", "items", func(s *Step) { s.Foreach.Items = " " }},
		{"empty body", "body", func(s *Step) { s.Foreach.Steps = nil }},
		{"invalid variable", "invalid shape", func(s *Step) { s.Foreach.As = "bad variable" }},
		{"negative concurrency", "parallelism", func(s *Step) { s.Foreach.Parallelism = -1 }},
		{"wait body", "not allowed", func(s *Step) { s.Foreach.Steps = []Step{{ID: "pause", Type: StepWait}} }},
		{"recursive call", "not allowed", func(s *Step) { s.Foreach.Steps = []Step{{ID: "child", Type: StepCallPipeline, PipelineSlug: "child"}} }},
		{"nested fanout", "nested foreach", func(s *Step) { s.Foreach.Steps = []Step{foreachBoundaryDSL().Steps[0]} }},
		{"duplicate ids", "duplicate", func(s *Step) { s.Foreach.Steps = append(s.Foreach.Steps, s.Foreach.Steps[0]) }},
		{"invalid HTTP body", "http", func(s *Step) { s.Foreach.Steps = []Step{{ID: "request", Type: StepHTTP}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := foreachBoundaryDSL()
			tc.change(&d.Steps[0])
			err := Validate(d, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("invalid foreach shape accepted: %v", err)
			}
		})
	}
}
