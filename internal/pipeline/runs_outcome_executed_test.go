package pipeline

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// A conditional agent_run that was skipped by its `if:` had nobody to report
// a hand-off: the run is judged by what executed, so a clean probe-only run
// is SUCCEEDED, not FAILED "no outcome reported". An agent that ran and
// reported nothing still fails the strict default.

func probeThenConditionalAgent() *DSL {
	return &DSL{Steps: []Step{
		{ID: "probe", Type: StepScript},
		{ID: "wake", Type: StepAgentRun, If: "{{ steps.probe.output.changed }}"},
	}}
}

func TestExecutedOutcomeCapableStep(t *testing.T) {
	foreachAgent := &DSL{Steps: []Step{
		{ID: "each", Type: StepForeach, Foreach: &ForeachStep{Steps: []Step{{ID: "inner", Type: StepAgentRun}}}},
	}}
	cases := []struct {
		name    string
		dsl     *DSL
		outputs map[string]string
		want    bool
	}{
		{"script only", &DSL{Steps: []Step{{ID: "s", Type: StepScript}}}, map[string]string{"s": "ok"}, false},
		{"agent skipped by if", probeThenConditionalAgent(), map[string]string{"probe": `{"changed":false}`, "wake": "<skipped>"}, false},
		{"agent never reached", probeThenConditionalAgent(), map[string]string{"probe": "ok"}, false},
		{"agent ran", probeThenConditionalAgent(), map[string]string{"probe": "ok", "wake": "done"}, true},
		{"agent ran with empty output", probeThenConditionalAgent(), map[string]string{"probe": "ok", "wake": ""}, true},
		{"nested routine ran", &DSL{Steps: []Step{{ID: "c", Type: StepCallPipeline}}}, map[string]string{"c": "child"}, true},
		{"foreach with agent ran", foreachAgent, map[string]string{"each": "[]"}, true},
		{"foreach skipped", foreachAgent, map[string]string{"each": "<skipped>"}, false},
		{"nil dsl", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExecutedOutcomeCapableStep(tc.dsl, tc.outputs); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkTerminal_SkippedConditionalAgent_IsSucceeded(t *testing.T) {
	cases := []struct {
		name        string
		outputs     map[string]string
		output      string
		wantOutcome string
		wantErr     string
	}{
		{"agent skipped", map[string]string{"probe": `{"changed":false}`, "wake": "<skipped>"}, `{"changed":false}`, orchestrator.OutcomeSucceeded, ""},
		{"agent ran without a hand-off", map[string]string{"probe": "ok", "wake": "I looked around."}, "I looked around.", orchestrator.OutcomeFailed, orchestrator.ReasonNoOutcomeReported},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, db := openRunsTestDB(t)
			defer db.Close()
			ctx := context.Background()
			id := []string{"run_skip", "run_silent"}[i]
			if err := store.Insert(ctx, &RunRecord{ID: id, WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "watch"}); err != nil {
				t.Fatalf("insert: %v", err)
			}
			ran := ExecutedOutcomeCapableStep(probeThenConditionalAgent(), tc.outputs)
			if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: id, Status: RunStatusCompleted, Output: tc.output, HasOutcomeCapableStep: ran}); err != nil {
				t.Fatalf("MarkTerminal: %v", err)
			}
			got, err := store.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != tc.wantOutcome || got.ErrorMessage != tc.wantErr {
				t.Fatalf("outcome=%q error=%q, want %q / %q", got.Outcome, got.ErrorMessage, tc.wantOutcome, tc.wantErr)
			}
		})
	}
}
