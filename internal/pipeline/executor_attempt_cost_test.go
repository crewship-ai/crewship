package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentStepIncludesSpendFromEveryAttempt(t *testing.T) {
	permanent := errors.New("agent failed after reporting usage")
	for _, tc := range []struct {
		name       string
		firstError error
		cancel     bool
		wantCost   float64
		wantCalls  int
		wantError  error
	}{
		{name: "empty reply then success", wantCost: 0.75, wantCalls: 2},
		{name: "transient error then success", firstError: errors.New("429 too many requests"), wantCost: 0.75, wantCalls: 2},
		{name: "permanent error with usage", firstError: permanent, wantCost: 0.25, wantCalls: 1, wantError: permanent},
		{name: "cancel during retry backoff", firstError: errors.New("429 too many requests"), cancel: true, wantCost: 0.25, wantCalls: 1, wantError: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			exec := retryHarness(t, func(context.Context, AgentStepRequest) (AgentStepResult, error) {
				calls++
				if calls == 1 {
					return AgentStepResult{CostUSD: 0.25}, tc.firstError
				}
				return AgentStepResult{Output: "classified", CostUSD: 0.50}, nil
			})
			exec.sleepFn = instantSleep
			if tc.cancel {
				exec.sleepFn = func(context.Context, time.Duration) bool {
					cancel()
					return false
				}
			}
			step := Step{ID: "classify", Type: StepAgentRun, AgentSlug: "classifier", OnFail: OnFailAbort}
			out, cost, _, err := exec.runAgentStep(ctx, step, "classify", AdapterModel{}, nil,
				RunInput{WorkspaceID: "ws_test"}, "run", "routine", &pipelineEmitContext{emitter: nopEmitter{}})
			if !errors.Is(err, tc.wantError) || cost != tc.wantCost || calls != tc.wantCalls {
				t.Fatalf("got err=%v cost=%v calls=%d, want err=%v cost=%v calls=%d", err, cost, calls, tc.wantError, tc.wantCost, tc.wantCalls)
			}
			if err == nil && out != "classified" {
				t.Fatalf("unexpected output %q", out)
			}
		})
	}
}

func TestRoutineBudgetCountsFailedAttemptBeforeRetry(t *testing.T) {
	calls := 0
	exec := retryHarness(t, func(context.Context, AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{CostUSD: 1}, errors.New("provider unavailable after reporting usage")
	})
	exec.sleepFn = instantSleep
	dsl := &DSL{Name: "cost-check", MaxCostUSD: 1.5, Steps: []Step{
		{ID: "classify", Type: StepAgentRun, AgentSlug: "classifier", Prompt: "Classify",
			OnFail: OnFailAbort, Retry: &RetryPolicy{MaxAttempts: 3}},
	}}
	res, err := exec.RunDefinition(t.Context(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" || res.CostUSD != 1 || calls != 1 || !strings.Contains(res.ErrorMessage, "cost cap") {
		t.Fatalf("budget should stop before the next attempt, got calls=%d status=%s cost=%v error=%q",
			calls, res.Status, res.CostUSD, res.ErrorMessage)
	}
}

func TestRoutineIncludesReportedSpendFromFailedGrader(t *testing.T) {
	exec := retryHarness(t, func(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
		if req.AgentSlug == "judge" {
			return AgentStepResult{CostUSD: 0.25}, errors.New("grader unavailable after reporting usage")
		}
		return AgentStepResult{Output: "classified", CostUSD: 0.50}, nil
	})
	dsl := &DSL{Name: "grader-cost", Steps: []Step{
		{ID: "classify", Type: StepAgentRun, AgentSlug: "classifier", Prompt: "Classify",
			Outcomes: &Outcomes{GraderAgentSlug: "judge", Criteria: []OutcomeCriterion{{Name: "quality", Rule: "correct"}}}},
	}}
	res, err := exec.RunDefinition(t.Context(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" || res.Output != "classified" || res.CostUSD != 0.75 {
		t.Fatalf("advisory grader failure should retain the result and both costs, got %+v", res)
	}
}
