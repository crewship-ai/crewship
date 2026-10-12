package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBehaviorDescriptionDistinguishesDeclaredAndEnforcedChecks(t *testing.T) {
	if DescribeBehavior(nil) != nil {
		t.Fatal("nil recipe invented a description")
	}
	minLength, maxLength := 2, 80
	validation := &Validation{Schema: json.RawMessage(`{"type":"object"}`), MinLength: &minLength, MaxLength: &maxLength, MustContain: []string{"ok"}, MustNotContain: []string{"secret"}}
	d := &DSL{MaxCostUSD: 3.5, Steps: []Step{
		{ID: "worker", Name: "Write result", Type: StepAgentRun, AgentSlug: "writer", TimeoutSec: 30, OnFail: OnFailAbort, Retry: &RetryPolicy{MaxAttempts: 2}, Validation: validation, Outcomes: &Outcomes{GraderAgentSlug: "reviewer", Criteria: []OutcomeCriterion{{Name: "correct", Rule: "matches request"}}, MaxIterations: 2, Required: true, OnFail: OnFailAbort}},
		{ID: "recover", Type: StepAgentRun, AgentSlug: "writer", OnFail: OnFailRetryStep, Outcomes: &Outcomes{GraderAgentSlug: "reviewer", OnFail: OnFailEscalateTier}},
		{ID: "child", Type: StepCallPipeline, PipelineSlug: "publish"},
		{ID: "approval", Type: StepWait, Wait: &WaitStep{Kind: "approval"}},
		{ID: "loop", Type: StepForeach, Foreach: &ForeachStep{Steps: []Step{{ID: "format", Type: StepTransform, Validation: validation, Outcomes: &Outcomes{GraderAgentSlug: "reviewer"}}}}},
	}}
	b := DescribeBehavior(d)
	if len(b.Steps) != 6 || !strings.Contains(b.Cost, "$3.5") || !strings.Contains(b.Scope, "not proof") {
		t.Fatalf("wrong scope or traversal: %+v", b)
	}
	worker := b.Steps[0]
	if worker.Name != "Write result" || worker.Performer != "Agent: writer" {
		t.Fatalf("wrong identity: %+v", worker)
	}
	for _, part := range []string{"JSON output schema", "Minimum output length: 2 bytes", "Maximum output length: 80 bytes", "1 required text checks", "1 forbidden text checks", "Required: an unavailable checker blocks", "correct: matches request"} {
		if !strings.Contains(strings.Join(worker.Checks, "\n"), part) {
			t.Errorf("missing check %q: %v", part, worker.Checks)
		}
	}
	for _, tc := range []struct{ value, part string }{
		{worker.Timeout, "30 seconds"}, {worker.Attempts, "Up to 2 step execution attempts"}, {worker.Attempts, "At most 2 worker/checker model tiers"},
		{worker.Failure, "structural check failure stops"}, {worker.Failure, "rejected checker verdict fails"},
		{b.Steps[1].Attempts, "step execution attempts"}, {b.Steps[1].Failure, "next configured model tier"},
		{strings.Join(b.Steps[1].Checks, "\n"), "Advisory availability"},
	} {
		if !strings.Contains(tc.value, tc.part) {
			t.Errorf("%q missing %q", tc.value, tc.part)
		}
	}
	if b.Steps[2].Performer != "Routine: publish" || b.Steps[3].Performer != "Wait for approval" || b.Steps[5].ID != "loop/format" {
		t.Fatalf("wrong call/wait/loop description: %+v", b.Steps)
	}
	checks := strings.Join(b.Steps[5].Checks, "\n")
	if !strings.Contains(checks, "structural checks are not enforced") || !strings.Contains(checks, "checker outcomes are not enforced") {
		t.Fatalf("description promised unsupported live checks: %s", checks)
	}
	plain := DescribeBehavior(&DSL{Steps: []Step{{ID: "plain", Type: StepTransform}}})
	if plain.Steps[0].Name != "plain" || !strings.Contains(plain.Cost, "No recipe cost cap") || !strings.Contains(plain.Steps[0].Checks[0], "No output acceptance checks") {
		t.Fatalf("defaults overpromise: %+v", plain)
	}
}
