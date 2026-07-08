package pipeline

import (
	"context"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// prewarm.go — warm the crew container at claim, off the critical path (#836).
// ---------------------------------------------------------------------------

func TestDSLUsesCrewContainer(t *testing.T) {
	cases := []struct {
		name string
		dsl  *DSL
		want bool
	}{
		{"agent_run", &DSL{Steps: []Step{{ID: "a", Type: StepAgentRun}}}, true},
		{"script", &DSL{Steps: []Step{{ID: "s", Type: StepScript}}}, true},
		{"http only", &DSL{Steps: []Step{{ID: "h", Type: StepHTTP}}}, false},
		{"transform only", &DSL{Steps: []Step{{ID: "t", Type: StepTransform}}}, false},
		{"call_pipeline only", &DSL{Steps: []Step{{ID: "c", Type: StepCallPipeline}}}, false},
		{"mixed http+agent", &DSL{Steps: []Step{{ID: "h", Type: StepHTTP}, {ID: "a", Type: StepAgentRun}}}, true},
		{"empty", &DSL{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dslUsesCrewContainer(c.dsl); got != c.want {
				t.Errorf("dslUsesCrewContainer(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// prewarmRunner is an AgentRunner that also implements CrewPrewarmer, recording
// the crew ids it was asked to warm.
type prewarmRunner struct {
	mu    sync.Mutex
	crews []string
}

func (r *prewarmRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{}, nil
}

func (r *prewarmRunner) PrewarmCrew(_ context.Context, crewID, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.crews = append(r.crews, crewID)
	return nil
}

func (r *prewarmRunner) warmed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.crews...)
}

func TestExecutor_PrewarmForRun_AgentRoutineWarmsAuthorCrew(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &prewarmRunner{}
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	in := validSaveInput("warm-me")
	in.DefinitionJSON = `{"name":"warm-me","steps":[{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	exec.PrewarmForRun(ctx, p.ID, "ws_test")

	got := runner.warmed()
	if len(got) != 1 || got[0] != "crew_a" {
		t.Fatalf("expected one PrewarmCrew for author crew crew_a, got %v", got)
	}
}

func TestExecutor_PrewarmForRun_AgentlessRoutineSkips(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &prewarmRunner{}
	exec := NewExecutor(store, resolver, runner, nil)
	ctx := context.Background()

	in := validSaveInput("no-container")
	in.DefinitionJSON = `{"name":"no-container","steps":[{"id":"t","type":"transform","transform":{"input":"1","expression":"."}}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	exec.PrewarmForRun(ctx, p.ID, "ws_test")

	if got := runner.warmed(); len(got) != 0 {
		t.Errorf("agentless routine must not prewarm any crew, got %v", got)
	}
}

func TestExecutor_PrewarmForRun_NonPrewarmerRunnerNoop(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	// newMockRunner is a plain AgentRunner, NOT a CrewPrewarmer — must no-op.
	exec := NewExecutor(store, resolver, newMockRunner(), nil)
	ctx := context.Background()

	in := validSaveInput("plain")
	in.DefinitionJSON = `{"name":"plain","steps":[{"id":"a","type":"agent_run","agent_slug":"x","prompt":"hi"}]}`
	p, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// Must not panic and must be a silent no-op.
	exec.PrewarmForRun(ctx, p.ID, "ws_test")
}

func TestExecutor_PrewarmForRun_UnknownPipelineNoop(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &prewarmRunner{}
	exec := NewExecutor(store, resolver, runner, nil)

	exec.PrewarmForRun(context.Background(), "pln_ghost", "ws_test")

	if got := runner.warmed(); len(got) != 0 {
		t.Errorf("unknown pipeline must not prewarm, got %v", got)
	}
}
