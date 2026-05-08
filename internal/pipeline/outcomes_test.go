package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestOutcomes_ValidationRejectsCallPipeline(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:           "a",
				Type:         StepCallPipeline,
				PipelineSlug: "inner",
				Outcomes: &Outcomes{
					Criteria:        []OutcomeCriterion{{Name: "x", Rule: "y"}},
					GraderAgentSlug: "judge",
				},
			},
		},
	}
	err := Validate(dsl, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "only supported on agent_run") {
		t.Errorf("expected outcomes-on-call_pipeline error, got %v", err)
	}
}

func TestOutcomes_ValidationRejectsEmptyCriteria(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Outcomes: &Outcomes{GraderAgentSlug: "judge"},
			},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent_lead": {}, "judge": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "criteria empty") {
		t.Errorf("expected empty-criteria error, got %v", err)
	}
}

func TestOutcomes_ValidationRejectsUnknownGrader(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Outcomes: &Outcomes{
					Criteria:        []OutcomeCriterion{{Name: "tone", Rule: "professional"}},
					GraderAgentSlug: "ghost",
				},
			},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent_lead": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "not found in author crew") {
		t.Errorf("expected unknown-grader error, got %v", err)
	}
}

func TestOutcomes_ValidationRejectsDuplicateCriterionName(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Outcomes: &Outcomes{
					Criteria: []OutcomeCriterion{
						{Name: "len", Rule: "short"},
						{Name: "len", Rule: "long"},
					},
					GraderAgentSlug: "judge",
				},
			},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent_lead": {}, "judge": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}

func TestOutcomes_ParseVerdict_PureJSON(t *testing.T) {
	raw := `{"passed":true,"per_criterion":{"tone":true,"length":true},"feedback":"good"}`
	criteria := []OutcomeCriterion{
		{Name: "tone", Rule: "professional"},
		{Name: "length", Rule: "100-500 chars"},
	}
	got, err := parseGraderVerdict(raw, criteria)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.passed {
		t.Errorf("passed: got %v", got.passed)
	}
	if got.feedback != "good" {
		t.Errorf("feedback: got %q", got.feedback)
	}
}

func TestOutcomes_ParseVerdict_FencedJSON(t *testing.T) {
	raw := "Here's my verdict:\n\n```json\n{\"passed\":false,\"per_criterion\":{\"tone\":false,\"length\":true},\"feedback\":\"too casual\"}\n```\n"
	criteria := []OutcomeCriterion{
		{Name: "tone", Rule: "professional"},
		{Name: "length", Rule: "100-500 chars"},
	}
	got, err := parseGraderVerdict(raw, criteria)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.passed {
		t.Errorf("expected fail, got pass")
	}
	if !strings.Contains(got.feedback, "too casual") {
		t.Errorf("feedback: %q", got.feedback)
	}
}

func TestOutcomes_ParseVerdict_MissingCriterionTreatedAsFail(t *testing.T) {
	// Grader's per_criterion lacks "length"; we treat that as fail
	// even though grader said passed=true — defensive consistency.
	raw := `{"passed":true,"per_criterion":{"tone":true},"feedback":"looks good"}`
	criteria := []OutcomeCriterion{
		{Name: "tone", Rule: "professional"},
		{Name: "length", Rule: "100-500 chars"},
	}
	got, err := parseGraderVerdict(raw, criteria)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.passed {
		t.Errorf("expected fail when criterion missing, got pass")
	}
	if !got.perCriterion["length"] == false {
		t.Errorf("missing criterion should be false; got %v", got.perCriterion)
	}
}

func TestOutcomes_ParseVerdict_NoJSON_Errors(t *testing.T) {
	_, err := parseGraderVerdict("just plain text, no JSON", []OutcomeCriterion{{Name: "x", Rule: "y"}})
	if err == nil {
		t.Error("expected error for non-JSON output")
	}
}

func TestOutcomes_ExecutorIntegration_PassFirstAttempt(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Worker output (first call), then grader verdict (second call).
	// mockRunner returns outputs by slug in order, so we set them per slug.
	runner.outputsBySlug["agent_lead"] = []string{"professional response of adequate length here"}
	// Add a grader agent fixture
	if _, err := store.db.ExecContext(context.Background(),
		`INSERT INTO agents (id, crew_id) VALUES ('judge_id', 'crew_a')`); err != nil {
		t.Fatalf("seed grader agent: %v", err)
	}
	// Update the agents table so the slug column resolves — schema in
	// store_test.go doesn't have slug; for this test we just need
	// the mockRunner to receive the grader call regardless of slug
	// resolution path. The mockRunner doesn't care; it returns by slug.
	runner.outputsBySlug["judge"] = []string{`{"passed":true,"per_criterion":{"tone":true},"feedback":"clean"}`}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "do x",
				Outcomes: &Outcomes{
					Criteria:        []OutcomeCriterion{{Name: "tone", Rule: "professional"}},
					GraderAgentSlug: "judge",
				},
			},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: got %q (err=%q)", res.Status, res.ErrorMessage)
	}
	// Confirm grader was actually called (worker + grader = 2 calls).
	if len(runner.calls) != 2 {
		t.Errorf("expected 2 runner calls (worker + grader), got %d", len(runner.calls))
	}
	if runner.calls[1].AgentSlug != "judge" {
		t.Errorf("second call should be grader, got slug=%q", runner.calls[1].AgentSlug)
	}
}

func TestOutcomes_ExecutorIntegration_FailRubricAborts(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"meh response"}
	runner.outputsBySlug["judge"] = []string{`{"passed":false,"per_criterion":{"tone":false},"feedback":"too casual"}`}
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)

	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "x",
				Outcomes: &Outcomes{
					Criteria:        []OutcomeCriterion{{Name: "tone", Rule: "professional"}},
					GraderAgentSlug: "judge",
					OnFail:          OnFailAbort,
				},
			},
		},
	}
	res, _ := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED for rubric miss, got %q", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "too casual") {
		t.Errorf("error should include grader feedback: %q", res.ErrorMessage)
	}
	// Validation_failed entry should be in the journal
	types := em.typesEmitted()
	found := false
	for _, t := range types {
		if t == journal.EntryPipelineStepValidation {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected validation_failed entry on rubric miss")
	}
}
