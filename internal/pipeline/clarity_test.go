package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestClarityInputContracts(t *testing.T) {
	minValue, maxValue := 0.0, 1.0
	for _, tc := range []struct {
		name  string
		input InputSpec
		value any
		valid bool
	}{
		{"fraction", InputSpec{Name: "coverage", Type: "number", Min: &minValue, Max: &maxValue}, 0.7, true},
		{"above bound without widget", InputSpec{Name: "coverage", Type: "number", Min: &minValue, Max: &maxValue}, 70.0, false},
		{"numeric string", InputSpec{Name: "coverage", Type: "number", Min: &minValue}, "0.7", false},
		{"absolute", InputSpec{Name: "dir", Type: "string", Format: "absolute_path"}, "/crew/shared/project", true},
		{"relative", InputSpec{Name: "dir", Type: "string", Format: "absolute_path"}, "project", false},
		{"traversal", InputSpec{Name: "dir", Type: "string", Format: "absolute_path"}, "/crew/../private", false},
		{"control", InputSpec{Name: "dir", Type: "string", Format: "absolute_path"}, "/crew/\nfile", false},
		{"legacy preserved", InputSpec{Name: "legacy", Type: "integer"}, "old-value", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormInputs(&DSL{Inputs: []InputSpec{tc.input}}, map[string]any{tc.input.Name: tc.value})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestClarityIterationLimitDrivesExecution(t *testing.T) {
	for _, limit := range []int{0, 1, 2} {
		t.Run(string(rune('0'+limit)), func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			runner := newMockRunner()
			runner.outputsBySlug["agent_lead"] = []string{"first", "second", "third"}
			runner.outputsBySlug["judge"] = []string{
				`{"passed":false,"per_criterion":{"correct":false},"feedback":"fix it"}`,
				`{"passed":false,"per_criterion":{"correct":false},"feedback":"still wrong"}`,
				`{"passed":true,"per_criterion":{"correct":true},"feedback":"ok"}`,
			}
			e := NewExecutor(store, resolver, runner, &captureEmitter{})
			step := Step{ID: "extract", Type: StepAgentRun, AgentSlug: "agent_lead", Outcomes: &Outcomes{Required: true, GraderAgentSlug: "judge", Criteria: []OutcomeCriterion{{Name: "correct", Rule: "matches source"}}, OnFail: OnFailEscalateTier, MaxIterations: limit}}
			emit := &pipelineEmitContext{emitter: &captureEmitter{}, workspaceID: "ws_test", pipelineID: "p", runID: "r"}
			_, _, _, err := e.runAgentStep(context.Background(), step, "extract", AdapterModel{}, []AdapterModel{{}, {}}, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a"}, "r", "p", emit)
			expected := limit
			if expected == 0 {
				expected = 3
			}
			if len(runner.calls) != expected*2 {
				t.Fatalf("limit %d: got %d calls, want %d", limit, len(runner.calls), expected*2)
			}
			if (err == nil) != (limit == 0) {
				t.Fatalf("limit %d: err=%v", limit, err)
			}
			if expected > 1 && !strings.Contains(runner.calls[2].Prompt, "fix it") {
				t.Fatal("checker feedback lost")
			}
		})
	}
}
