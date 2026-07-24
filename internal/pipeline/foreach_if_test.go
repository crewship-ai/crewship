package pipeline

import (
	"context"
	"sync"
	"testing"
)

// echoRunner returns the rendered prompt as the step output, so a foreach body
// step with prompt "{{ inputs.item }}" echoes the current item. Counts calls.
type echoRunner struct {
	mu    sync.Mutex
	count int
}

func (e *echoRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	e.mu.Lock()
	e.count++
	e.mu.Unlock()
	return AgentStepResult{Output: req.Prompt, CostUSD: 0.001, DurationMs: 5}, nil
}

func (e *echoRunner) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

// #1419, part 1 — a foreach over ["a","b"] runs the body once per item and
// collects the per-item outputs into a JSON array (input order preserved).
func TestExecutor_Foreach_FansOutAndCollects(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &echoRunner{}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{
		Name: "fan",
		Steps: []Step{
			{
				ID:   "each",
				Type: StepForeach,
				Foreach: &ForeachStep{
					Items: `{{ inputs.letters }}`,
					As:    "item",
					Steps: []Step{
						{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "{{ inputs.item }}"},
					},
				},
			},
		},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		Inputs:       map[string]any{"letters": []any{"a", "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s (%s)", res.Status, res.ErrorMessage)
	}
	if runner.calls() != 2 {
		t.Errorf("expected body to run twice, ran %d", runner.calls())
	}
	if got := res.StepOutputs["each"]; got != `["a","b"]` {
		t.Errorf("expected collected array [\"a\",\"b\"], got %q", got)
	}
}

// #1419, part 1 — items rendered from a nested-object array collect as
// structured JSON, and Parallelism:1 keeps sequential order deterministic.
func TestExecutor_Foreach_StructuredItems(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &echoRunner{}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{
		Name: "fan2",
		Steps: []Step{{
			ID:   "each",
			Type: StepForeach,
			Foreach: &ForeachStep{
				Items:       `[{"n":1},{"n":2}]`,
				As:          "row",
				Parallelism: 1,
				Steps: []Step{
					{ID: "w", Type: StepAgentRun, AgentSlug: "worker", Prompt: "{{ inputs.row.n }}"},
				},
			},
		}},
	}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s (%s)", res.Status, res.ErrorMessage)
	}
	// Each echo output is "1"/"2" (a bare number string), which is valid JSON
	// and collects as numbers.
	if got := res.StepOutputs["each"]; got != `[1,2]` {
		t.Errorf("expected [1,2], got %q", got)
	}
}

// #1419, part 2 — CEL in `if:` gates correctly, and a bare truthy string still
// works via the back-compat fallback.
func TestExecutor_If_CEL_And_Fallback(t *testing.T) {
	cases := []struct {
		name    string
		ifExpr  string
		inputs  map[string]any
		wantRun bool
	}{
		{"cel true", `inputs.x == "y"`, map[string]any{"x": "y"}, true},
		{"cel false", `inputs.x == "y"`, map[string]any{"x": "n"}, false},
		{"cel numeric", `inputs.n > 3`, map[string]any{"n": 5}, true},
		{"bare truthy string", "yes", nil, true},
		{"bare falsey string", "false", nil, false},
		{"template truthy", "{{ inputs.flag }}", map[string]any{"flag": "1"}, true},
		{"template falsey", "{{ inputs.flag }}", map[string]any{"flag": ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store, resolver, cleanup := openExecutorTestDB(t)
			defer cleanup()
			runner := newMockRunner()
			exec := NewExecutor(store, resolver, runner, nil)

			dsl := &DSL{
				Name: "gate",
				Steps: []Step{
					{ID: "s1", Type: StepAgentRun, AgentSlug: "a", Prompt: "go", If: c.ifExpr},
				},
			}
			res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
				WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun, Inputs: c.inputs,
			})
			if err != nil {
				t.Fatal(err)
			}
			ran := len(runner.calls) == 1
			if ran != c.wantRun {
				t.Errorf("if %q with %v: ran=%v want=%v (output=%q)", c.ifExpr, c.inputs, ran, c.wantRun, res.StepOutputs["s1"])
			}
			if !c.wantRun && res.StepOutputs["s1"] != "<skipped>" {
				t.Errorf("expected <skipped> output, got %q", res.StepOutputs["s1"])
			}
		})
	}
}

// evalStepCondition unit coverage for the CEL steps namespace + fallback edges.
func TestEvalStepCondition_Units(t *testing.T) {
	ctx := RenderContext{
		Inputs:      map[string]any{"x": "y"},
		StepOutputs: map[string]string{"classify": "spam"},
		Env:         map[string]string{"is_replay": "true"},
	}
	cases := []struct {
		expr string
		want bool
	}{
		{`inputs.x == "y"`, true},
		{`inputs.x == "z"`, false},
		{`steps.classify == "spam"`, true},
		{`steps.classify == "ham"`, false},
		{`run.is_replay`, true},
		{"", true},       // no condition ⇒ run
		{"yes", true},    // bare truthy fallback
		{"off", false},   // bare falsey fallback
		{"true", true},   // CEL bool literal
		{"false", false}, // CEL bool literal
	}
	for _, c := range cases {
		if got := evalStepCondition(c.expr, ctx); got != c.want {
			t.Errorf("evalStepCondition(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

// Security (go/uncontrolled-allocation-size): a foreach over an untrusted
// array must be bounded — an oversized items array is rejected before the
// per-item allocation/fan-out rather than driving an unbounded run.
func TestExecutor_Foreach_RejectsOversizedArray(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &echoRunner{}
	exec := NewExecutor(store, resolver, runner, nil)

	big := make([]any, maxForeachItems+1)
	for i := range big {
		big[i] = "x"
	}
	dsl := &DSL{
		Name: "toobig",
		Steps: []Step{{
			ID:   "each",
			Type: StepForeach,
			Foreach: &ForeachStep{
				Items: `{{ inputs.arr }}`,
				As:    "item",
				Steps: []Step{{ID: "work", Type: StepAgentRun, AgentSlug: "worker", Prompt: "{{ inputs.item }}"}},
			},
		}},
	}
	res, _ := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		Inputs:       map[string]any{"arr": big},
	})
	if res.Status == "COMPLETED" {
		t.Fatalf("expected the oversized foreach to fail, got COMPLETED")
	}
	if runner.calls() != 0 {
		t.Errorf("body must not run for an over-cap array, ran %d", runner.calls())
	}
}
