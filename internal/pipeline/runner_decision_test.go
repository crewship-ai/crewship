package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/decisions"
	"github.com/crewship-ai/crewship/internal/journal"
)

type fakeDecisionEvaluator struct {
	choice string
	p      float64
	err    error
	seen   decisions.Request
}

func (f *fakeDecisionEvaluator) Evaluate(_ context.Context, req decisions.Request) (decisions.Response, error) {
	f.seen = req
	if f.err != nil {
		return decisions.Response{}, f.err
	}
	var options map[string]string
	_ = json.Unmarshal(req.Questions["route"].Criteria, &options)
	other := (1 - f.p) / float64(len(options)-1)
	probs := make(map[string]*float64, len(options))
	for option := range options {
		value := other
		if option == f.choice {
			value = f.p
		}
		probs[option] = &value
	}
	zero := int64(0)
	return decisions.Response{
		Model: "fake", Answers: map[string]decisions.Answer{
			"route": {Type: "choice", Choice: f.choice, Confidence: &f.p, Probabilities: probs},
		}, Usage: decisions.Usage{InputTokens: &zero, OutputTokens: &zero},
	}, nil
}

func TestDecisionStep_ExampleWebhookRoutine(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/jev-eval/webhook-router.routine.json")
	if err != nil {
		t.Fatal(err)
	}
	dsl, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(dsl, map[string]struct{}{"sre": {}, "developer": {}}, nil); err != nil {
		t.Fatal(err)
	}
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["sre"] = []string{"investigating"}
	model := &fakeDecisionEvaluator{choice: "sre", p: .97}
	exec := NewExecutor(store, resolver, runner, nil).WithDecisionEvaluator(model, "api.typesafe.ai")
	res, err := exec.RunDefinition(t.Context(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun, TriggeredVia: TriggeredViaWebhook,
		Inputs: map[string]any{"event": map[string]any{"type": "alert", "service": "api", "summary": "deployment failed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" || res.StepOutputs["route"] != "sre" || res.StepOutputs["wake_sre"] != "investigating" || res.StepOutputs["wake_developer"] != "<skipped>" || len(runner.calls) != 1 {
		t.Fatalf("example did not route exactly one agent: status=%s outputs=%v calls=%v", res.Status, res.StepOutputs, runner.calls)
	}
}

func TestDecisionStep_ExampleWebhookReviewWaitsForApproval(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/jev-eval/webhook-router.routine.json")
	if err != nil {
		t.Fatal(err)
	}
	db := openResumeTestDB(t)
	defer db.Close()
	store := NewStore(db)
	input := validSaveInput("webhook-decision-router")
	input.DefinitionJSON = string(raw)
	pipeline, err := store.Save(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	waitpoints := NewSQLWaitpointStore(db)
	defer waitpoints.Close()
	runner := newMockRunner()
	model := &fakeDecisionEvaluator{choice: "sre", p: .6}
	exec := NewExecutor(store, NewResolver(db), runner, &captureEmitter{}).
		WithRunStore(NewRunStore(db)).
		WithWaitpointStore(waitpoints).
		WithDecisionEvaluator(model, "api.typesafe.ai")
	res, err := exec.Run(t.Context(), RunInput{
		PipelineID: pipeline.ID, WorkspaceID: "ws_test", Mode: ModeRun,
		TriggeredVia: TriggeredViaWebhook,
		Inputs: map[string]any{"event": map[string]any{
			"type": "alert", "service": "api", "summary": "unclear incident", "secret": "never-forward-this",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "WAITING" || res.CurrentStep != "request_review" || res.WaitpointToken == "" {
		t.Fatalf("review did not park on approval: status=%s step=%s token=%q", res.Status, res.CurrentStep, res.WaitpointToken)
	}
	if res.StepOutputs["route"] != "review" || len(runner.calls) != 0 {
		t.Fatalf("uncertain webhook woke an agent: outputs=%v calls=%v", res.StepOutputs, runner.calls)
	}
	if strings.Contains(string(model.seen.State), "never-forward-this") {
		t.Fatal("decision state forwarded an unselected event field")
	}
	var status, stepID string
	if err := db.QueryRowContext(t.Context(),
		`SELECT status, step_id FROM pipeline_waitpoints WHERE token = ?`, res.WaitpointToken,
	).Scan(&status, &stepID); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || stepID != "request_review" {
		t.Fatalf("approval status=%q step=%q", status, stepID)
	}
}

func decisionTestDSL() *DSL {
	return &DSL{Name: "webhook-router", EgressTargets: []string{"api.typesafe.ai"}, Inputs: []InputSpec{{Name: "event", Type: "object"}}, Steps: []Step{
		{ID: "route", Type: StepDecision, Decision: &DecisionStep{
			State: "{{ inputs.event }}", Instructions: "Select the incident owner.",
			Options: map[string]string{"sre": "Operational outage", "review": "Unclear or other event"},
		}},
		{ID: "wake", Type: StepAgentRun, Needs: []string{"route"}, AgentSlug: "sre", Prompt: "Investigate {{ inputs.event }}", If: `steps.route == "sre"`},
	}}
}

func TestDecisionStep_WebhookRouteAndReview(t *testing.T) {
	for _, tc := range []struct {
		name, choice, wantRoute, wantWake string
		p                                 float64
	}{
		{"clear incident", "sre", "sre", "investigating", .96},
		{"uncertain incident", "sre", "review", "<skipped>", .60},
		{"explicit review", "review", "review", "<skipped>", .97},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			runner := newMockRunner()
			runner.outputsBySlug["sre"] = []string{"investigating"}
			model := &fakeDecisionEvaluator{choice: tc.choice, p: tc.p}
			emitter := &captureEmitter{}
			exec := NewExecutor(store, resolver, runner, emitter).WithDecisionEvaluator(model, "api.typesafe.ai")
			dsl := decisionTestDSL()
			if err := Validate(dsl, map[string]struct{}{"sre": {}}, nil); err != nil {
				t.Fatalf("invalid routine: %v", err)
			}
			res, err := exec.RunDefinition(t.Context(), dsl, RunInput{
				WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
				TriggeredVia: TriggeredViaWebhook,
				Inputs:       map[string]any{"event": map[string]any{"message": "deployment failed"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != "COMPLETED" || res.StepOutputs["route"] != tc.wantRoute || res.StepOutputs["wake"] != tc.wantWake {
				t.Fatalf("run status=%s outputs=%v", res.Status, res.StepOutputs)
			}
			if !strings.Contains(string(model.seen.State), "deployment failed") {
				t.Fatalf("model did not see selected event: %s", model.seen.State)
			}
			if strings.Contains(model.seen.Questions["route"].Instructions, "deployment failed") {
				t.Fatal("untrusted event was inserted into model instructions")
			}
			if tc.wantWake == "<skipped>" && len(runner.calls) != 0 {
				t.Fatalf("uncertain event woke an agent: %v", runner.calls)
			}
			found := false
			for _, entry := range emitter.entries {
				if entry.Type == journal.EntryPipelineDecisionEvaluated {
					found = true
					if entry.Payload["selected"] != tc.wantRoute || strings.Contains(entry.Summary, "deployment failed") {
						t.Fatalf("decision journal entry = %+v", entry)
					}
				}
			}
			if !found {
				t.Fatal("decision was not recorded in journal")
			}
		})
	}
}

func TestDecisionStep_EgressGateStopsProviderCall(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	model := &fakeDecisionEvaluator{choice: "sre", p: .99}
	exec := NewExecutor(store, resolver, runner, nil).WithDecisionEvaluator(model, "api.typesafe.ai")
	dsl := decisionTestDSL()
	dsl.EgressTargets = []string{"example.com"}
	res, err := exec.RunDefinition(t.Context(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" || len(model.seen.State) != 0 || len(runner.calls) != 0 {
		t.Fatalf("undeclared provider host must fail before call: status=%s state=%s calls=%v", res.Status, model.seen.State, runner.calls)
	}
}

func TestDecisionStep_CrewPolicyStopsProviderCall(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	model := &fakeDecisionEvaluator{choice: "sre", p: .99}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithDecisionEvaluator(model, "api.typesafe.ai")
	exec.WithEgressGate(func(_ context.Context, scope RunScope, host string) error {
		if host != "api.typesafe.ai" || !scope.WebhookTriggered || !scope.RoutineDeclaresEgress {
			t.Fatalf("egress scope: host=%q scope=%+v", host, scope)
		}
		return errors.New("crew policy denied")
	})
	res, err := exec.RunDefinition(t.Context(), decisionTestDSL(), RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun, TriggeredVia: TriggeredViaWebhook,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" || len(model.seen.State) != 0 {
		t.Fatalf("crew policy must block provider call: status=%s state=%s", res.Status, model.seen.State)
	}
}

func TestDecisionStep_WorkspaceScopeStopsProviderCall(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	model := &fakeDecisionEvaluator{choice: "sre", p: .99}
	exec := NewExecutor(store, resolver, newMockRunner(), nil).WithDecisionEvaluator(model, "api.typesafe.ai")
	exec.decisionWorkspaceID = "another-workspace"
	res, err := exec.RunDefinition(t.Context(), decisionTestDSL(), RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" || len(model.seen.State) != 0 {
		t.Fatalf("foreign workspace must not call provider: status=%s state=%s", res.Status, model.seen.State)
	}
}

func TestDecisionStep_ProviderFailureStopsRun(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil).WithDecisionEvaluator(&fakeDecisionEvaluator{err: errors.New("unavailable")}, "api.typesafe.ai")
	res, err := exec.RunDefinition(t.Context(), decisionTestDSL(), RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" || len(runner.calls) != 0 {
		t.Fatalf("provider failure must not wake an agent: status=%s calls=%v", res.Status, runner.calls)
	}
}

func TestDecisionStep_RequiresReviewOption(t *testing.T) {
	dsl := decisionTestDSL()
	delete(dsl.Steps[0].Decision.Options, "review")
	dsl.Steps[0].Decision.Options["other"] = "Another destination"
	if err := Validate(dsl, nil, nil); err == nil || !strings.Contains(err.Error(), "review option") {
		t.Fatalf("validation = %v", err)
	}
}

func TestDecisionStep_RejectsWebhookHeadersAndRawBody(t *testing.T) {
	for _, state := range []string{"{{ inputs.headers }}", "{{  inputs.headers.X-Crewship-Signature  }}", "{{ inputs.raw }}"} {
		dsl := decisionTestDSL()
		dsl.Steps[0].Decision.State = state
		if err := Validate(dsl, nil, nil); err == nil || !strings.Contains(err.Error(), "raw webhook bytes or headers") {
			t.Fatalf("state %q validation = %v", state, err)
		}
	}
}
