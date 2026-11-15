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

// End to end through the executor: the terminal write judges the run by the
// steps that executed. A probe whose condition skips the agent settles to
// SUCCEEDED; the same routine with the agent running and reporting no
// hand-off settles to FAILED "no outcome reported".
func TestExecutor_SkippedConditionalAgentOutcome(t *testing.T) {
	cases := []struct {
		name, changed, wantOutcome, wantErr string
	}{
		{"agent skipped", "false", orchestrator.OutcomeSucceeded, ""},
		{"agent ran without a hand-off", "true", orchestrator.OutcomeFailed, orchestrator.ReasonNoOutcomeReported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openResumeTestDB(t)
			t.Cleanup(func() { db.Close() })
			store := NewStore(db)
			runStore := NewRunStore(db)
			dsl := `{"dsl_version":"1.0","name":"watch","steps":[
 {"id":"probe","type":"transform","transform":{"input":"{\"changed\":` + tc.changed + `}","expression":"."}},
 {"id":"wake","type":"agent_run","agent_slug":"agent_lead","prompt":"look","if":"{{ steps.probe.output.changed }}"}
]}`
			p := saveResumePipeline(t, store, "watch-"+tc.changed, dsl)
			exec := NewExecutor(store, NewResolver(db), newMockRunner(), &captureEmitter{}).WithRunStore(runStore)
			res, err := exec.Run(context.Background(), RunInput{PipelineID: p.ID, WorkspaceID: "ws_test", Mode: ModeRun})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			got, err := runStore.Get(context.Background(), res.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Outcome != tc.wantOutcome || got.ErrorMessage != tc.wantErr {
				t.Fatalf("outcome=%q error=%q (wake=%q), want %q / %q", got.Outcome, got.ErrorMessage, res.StepOutputs["wake"], tc.wantOutcome, tc.wantErr)
			}
		})
	}
}
