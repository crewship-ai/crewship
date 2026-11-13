package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// The outcome contract judges a routine by what it executed, not by what it
// declared. These cases are the ones a device-monitoring deployment hit: a
// conditional agent_run that was correctly skipped, and an agent hand-off
// followed by a crewship step that returned comment JSON.

const checkpointNeedsHuman = "---CHECKPOINT---\nsummary: host offline\noutcome: NEEDS_HUMAN\n---END CHECKPOINT---"

func probeThenConditionalAgent() *DSL {
	return &DSL{Steps: []Step{
		{ID: "probe", Type: StepScript},
		{ID: "wake", Type: StepAgentRun, If: "{{ steps.probe.output.changed }}"},
	}}
}

func TestExecutedOutcomeSources(t *testing.T) {
	foreachAgent := &DSL{Steps: []Step{
		{ID: "each", Type: StepForeach, Foreach: &ForeachStep{Steps: []Step{{ID: "inner", Type: StepAgentRun}}}},
	}}
	cases := []struct {
		name    string
		dsl     *DSL
		outputs map[string]string
		wantRan bool
		wantN   int
	}{
		{"script only", &DSL{Steps: []Step{{ID: "s", Type: StepScript}}}, map[string]string{"s": "ok"}, false, 0},
		{"agent skipped by if", probeThenConditionalAgent(), map[string]string{"probe": `{"changed":false}`, "wake": "<skipped>"}, false, 0},
		{"agent never reached", probeThenConditionalAgent(), map[string]string{"probe": "ok"}, false, 0},
		{"agent ran", probeThenConditionalAgent(), map[string]string{"probe": "ok", "wake": checkpointNeedsHuman}, true, 1},
		{"agent ran with empty output", probeThenConditionalAgent(), map[string]string{"probe": "ok", "wake": ""}, true, 1},
		{"nested routine ran", &DSL{Steps: []Step{{ID: "c", Type: StepCallPipeline}}}, map[string]string{"c": "child"}, true, 1},
		{"foreach with agent ran", foreachAgent, map[string]string{"each": "[]"}, true, 1},
		{"foreach skipped", foreachAgent, map[string]string{"each": "<skipped>"}, false, 0},
		{"nil dsl", nil, nil, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran, src := ExecutedOutcomeSources(tc.dsl, tc.outputs)
			if ran != tc.wantRan || len(src) != tc.wantN {
				t.Fatalf("ran=%v sources=%d, want ran=%v sources=%d", ran, len(src), tc.wantRan, tc.wantN)
			}
		})
	}
}

func TestDeriveRunOutcome_ExecutedContract(t *testing.T) {
	commentJSON, _ := json.Marshal(map[string]any{"id": "cmt_1", "body": "Result:\n" + checkpointNeedsHuman})
	cases := []struct {
		name        string
		status      RunStatus
		output      string
		ran         bool
		sources     []string
		wantOutcome string
		wantReason  string
	}{
		{name: "script-only success", status: RunStatusCompleted, output: "probe ok", wantOutcome: orchestrator.OutcomeSucceeded},
		{name: "agent declared but skipped", status: RunStatusCompleted, output: `{"changed":false}`, ran: false, wantOutcome: orchestrator.OutcomeSucceeded},
		{name: "agent ran and reported", status: RunStatusCompleted, output: checkpointNeedsHuman, ran: true, sources: []string{checkpointNeedsHuman}, wantOutcome: orchestrator.OutcomeNeedsHuman},
		{name: "agent ran without a hand-off", status: RunStatusCompleted, output: "I looked around.", ran: true, sources: []string{"I looked around."}, wantOutcome: orchestrator.OutcomeFailed, wantReason: orchestrator.ReasonNoOutcomeReported},
		{name: "agent ran with malformed hand-off", status: RunStatusCompleted, output: "---CHECKPOINT---\noutcome: GREAT\n---END CHECKPOINT---", ran: true, sources: []string{"---CHECKPOINT---\noutcome: GREAT\n---END CHECKPOINT---"}, wantOutcome: orchestrator.OutcomeFailed, wantReason: orchestrator.ReasonNoOutcomeReported},
		{name: "hand-off inside final comment JSON", status: RunStatusCompleted, output: string(commentJSON), ran: true, sources: []string{"no markers in this agent output"}, wantOutcome: orchestrator.OutcomeNeedsHuman},
		{name: "agent hand-off then crewship JSON without markers", status: RunStatusCompleted, output: `{"id":"cmt_2","body":"posted"}`, ran: true, sources: []string{checkpointNeedsHuman}, wantOutcome: orchestrator.OutcomeNeedsHuman},
		{name: "failed run is never rescued by a hand-off", status: RunStatusFailed, output: checkpointNeedsHuman, ran: true, sources: []string{checkpointNeedsHuman}, wantOutcome: orchestrator.OutcomeFailed},
		{name: "cancelled run stays cancelled", status: RunStatusCancelled, output: "", ran: true, wantOutcome: orchestrator.OutcomeCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := deriveRunOutcome(tc.status, tc.output, "", tc.ran, tc.sources...)
			if got != tc.wantOutcome || reason != tc.wantReason {
				t.Fatalf("outcome=%q reason=%q, want %q / %q", got, reason, tc.wantOutcome, tc.wantReason)
			}
		})
	}
}

// A JSON string that merely mentions a marker word without the real block
// must not produce an outcome: only decoded strings are parsed.
func TestReportedOutcomeIn_DoesNotFabricateFromEscapedText(t *testing.T) {
	if v := reportedOutcomeIn(`{"body":"outcome: SUCCEEDED"}`); v != "" {
		t.Fatalf("got %q from a body without CHECKPOINT/HANDOFF markers", v)
	}
	if v := reportedOutcomeIn(`not json ---CHECKPOINT---\noutcome: SUCCEEDED\n---END CHECKPOINT---`); v != "" {
		t.Fatalf("got %q from escaped newlines outside JSON", v)
	}
}

// End-to-end through MarkTerminal: the stored row carries the contract.
func TestMarkTerminal_SkippedConditionalAgent_IsSucceeded(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()
	if err := store.Insert(ctx, &RunRecord{ID: "run_skip", WorkspaceID: "ws_runs", PipelineID: "pln_a", PipelineSlug: "watch"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ran, sources := ExecutedOutcomeSources(probeThenConditionalAgent(), map[string]string{"probe": `{"changed":false}`, "wake": "<skipped>"})
	if err := store.MarkTerminal(ctx, MarkTerminalInput{RunID: "run_skip", Status: RunStatusCompleted, Output: `{"changed":false}`, HasOutcomeCapableStep: ran, OutcomeSources: sources}); err != nil {
		t.Fatalf("MarkTerminal: %v", err)
	}
	got, err := store.Get(ctx, "run_skip")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != orchestrator.OutcomeSucceeded || got.ErrorMessage != "" {
		t.Fatalf("outcome=%q error=%q, want SUCCEEDED with no error", got.Outcome, got.ErrorMessage)
	}
}
