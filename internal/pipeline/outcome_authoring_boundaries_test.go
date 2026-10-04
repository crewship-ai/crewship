package pipeline

import (
	"fmt"
	"strings"
	"testing"
)

func TestOutcomeAuthoringRejectsUnsatisfiableOrUnattributedRubrics(t *testing.T) {
	roster := map[string]struct{}{"worker": {}, "grader": {}}
	for _, tc := range []struct {
		name, part string
		edit       func(*Outcomes)
	}{
		{"missing grader", "grader_agent_slug", func(o *Outcomes) { o.GraderAgentSlug = "" }},
		{"invalid grader", "invalid shape", func(o *Outcomes) { o.GraderAgentSlug = "bad grader" }},
		{"foreign grader", "not found", func(o *Outcomes) { o.GraderAgentSlug = "foreign" }},
		{"empty rubric", "criteria empty", func(o *Outcomes) { o.Criteria = nil }},
		{"oversized rubric", "too long", func(o *Outcomes) {
			for i := 1; i < 21; i++ {
				o.Criteria = append(o.Criteria, OutcomeCriterion{Name: fmt.Sprintf("rule%d", i), Rule: "valid"})
			}
		}},
		{"unnamed rule", "missing name", func(o *Outcomes) { o.Criteria[0].Name = "" }},
		{"empty rule", "missing rule", func(o *Outcomes) { o.Criteria[0].Rule = "" }},
		{"duplicate rule", "duplicate name", func(o *Outcomes) { o.Criteria = append(o.Criteria, o.Criteria[0]) }},
		{"negative iterations", "negative", func(o *Outcomes) { o.MaxIterations = -1 }},
		{"unbounded iterations", "too high", func(o *Outcomes) { o.MaxIterations = 11 }},
		{"unknown failure policy", "on_fail", func(o *Outcomes) { o.OnFail = "ignore" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &Outcomes{GraderAgentSlug: "grader", Criteria: []OutcomeCriterion{{Name: "quality", Rule: "must answer"}}}
			tc.edit(out)
			d := &DSL{Name: "graded", Steps: []Step{{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "Complete the task", Outcomes: out}}}
			err := Validate(d, roster, nil)
			if err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("bad rubric escaped authoring: %v", err)
			}
		})
	}
	out := &Outcomes{GraderAgentSlug: "grader", Criteria: []OutcomeCriterion{{Name: "quality", Rule: "must answer"}}, MaxIterations: 10, OnFail: OnFailRetryStep}
	d := &DSL{Name: "graded", Steps: []Step{{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "Complete the task", Outcomes: out}}}
	if err := Validate(d, roster, nil); err != nil {
		t.Fatalf("bounded attributed rubric refused: %v", err)
	}
}
