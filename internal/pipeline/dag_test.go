package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHasNeeds(t *testing.T) {
	t.Run("no needs anywhere", func(t *testing.T) {
		dsl := &DSL{Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "x"},
			{ID: "b", Type: StepAgentRun, AgentSlug: "x"},
		}}
		if hasNeeds(dsl) {
			t.Errorf("expected false")
		}
	})
	t.Run("one step with needs flips on", func(t *testing.T) {
		dsl := &DSL{Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "x"},
			{ID: "b", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"a"}},
		}}
		if !hasNeeds(dsl) {
			t.Errorf("expected true")
		}
	})
}

func TestValidateDAG_DetectsCycle(t *testing.T) {
	dsl := &DSL{Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"b"}},
		{ID: "b", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"a"}},
	}}
	err := validateDAG(dsl)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestValidateDAG_DetectsSelfLoop(t *testing.T) {
	dsl := &DSL{Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"a"}},
	}}
	err := validateDAG(dsl)
	if err == nil || !strings.Contains(err.Error(), "depends on itself") {
		t.Errorf("expected self-loop error, got %v", err)
	}
}

func TestValidateDAG_DetectsUnknownNeed(t *testing.T) {
	dsl := &DSL{Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"ghost"}},
	}}
	err := validateDAG(dsl)
	if err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("expected unknown-step error, got %v", err)
	}
}

func TestValidateDAG_HappyPath(t *testing.T) {
	dsl := &DSL{Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "x"},
		{ID: "b", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"a"}},
		{ID: "c", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"a"}},
		{ID: "d", Type: StepAgentRun, AgentSlug: "x", Needs: []string{"b", "c"}},
	}}
	if err := validateDAG(dsl); err != nil {
		t.Errorf("expected diamond DAG to validate, got %v", err)
	}
}

// trackingRunner is a mockRunner that records when each step started
// and finished, so parallel-execution tests can verify two
// independent steps actually overlapped in time.
type trackingRunner struct {
	mu       sync.Mutex
	timeline map[string][2]time.Time // slug → [start, end]
	delay    time.Duration
}

func newTrackingRunner(delay time.Duration) *trackingRunner {
	return &trackingRunner{
		timeline: map[string][2]time.Time{},
		delay:    delay,
	}
}

func (r *trackingRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	start := time.Now()
	time.Sleep(r.delay)
	end := time.Now()
	r.mu.Lock()
	r.timeline[req.AgentSlug] = [2]time.Time{start, end}
	r.mu.Unlock()
	return AgentStepResult{Output: "out:" + req.AgentSlug, CostUSD: 0.001}, nil
}

func TestExecutor_DAG_RunsIndependentStepsInParallel(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	const stepDelay = 100 * time.Millisecond
	runner := newTrackingRunner(stepDelay)
	exec := NewExecutor(store, resolver, runner, nil)

	// Diamond DAG: a → {b,c} → d. b and c must overlap in time.
	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_a", Prompt: ""},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_b", Prompt: "", Needs: []string{"a"}},
		{ID: "c", Type: StepAgentRun, AgentSlug: "agent_c", Prompt: "", Needs: []string{"a"}},
		{ID: "d", Type: StepAgentRun, AgentSlug: "agent_d", Prompt: "", Needs: []string{"b", "c"}},
	}}

	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s err=%s", res.Status, res.ErrorMessage)
	}

	// Verify b and c overlapped: b's end > c's start (or vice versa)
	runner.mu.Lock()
	bSpan := runner.timeline["agent_b"]
	cSpan := runner.timeline["agent_c"]
	runner.mu.Unlock()
	overlap := bSpan[0].Before(cSpan[1]) && cSpan[0].Before(bSpan[1])
	if !overlap {
		t.Errorf("expected b and c to overlap; b=%v c=%v", bSpan, cSpan)
	}
}

func TestExecutor_DAG_RespectsDependencies(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newTrackingRunner(20 * time.Millisecond)
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_a", Prompt: ""},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_b", Prompt: "", Needs: []string{"a"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %s", res.Status)
	}
	runner.mu.Lock()
	aEnd := runner.timeline["agent_a"][1]
	bStart := runner.timeline["agent_b"][0]
	runner.mu.Unlock()
	if !bStart.After(aEnd) {
		t.Errorf("expected b to start after a finished; a-end=%v b-start=%v", aEnd, bStart)
	}
}

func TestExecutor_DAG_FailFast(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// agent_b will fail; we want d (which needs b+c) to never run
	runner.errBySlug["agent_b"] = []error{errors.New("boom")}
	runner.outputsBySlug["agent_a"] = []string{"a-ok"}
	runner.outputsBySlug["agent_c"] = []string{"c-ok"}
	runner.outputsBySlug["agent_d"] = []string{"d-should-not-run"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_a"},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_b", Needs: []string{"a"}},
		{ID: "c", Type: StepAgentRun, AgentSlug: "agent_c", Needs: []string{"a"}},
		{ID: "d", Type: StepAgentRun, AgentSlug: "agent_d", Needs: []string{"b", "c"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED, got %s", res.Status)
	}
	if res.FailedAtStep != "b" {
		t.Errorf("expected FailedAtStep=b, got %s", res.FailedAtStep)
	}
	// d must not have run — b's failure cancelled the DAG before d became ready
	for _, c := range runner.calls {
		if c.AgentSlug == "agent_d" {
			t.Errorf("d should not have executed (its dependency b failed)")
		}
	}
}

func TestExecutor_DAG_CallPipelineRejected(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_a"},
		{ID: "b", Type: StepCallPipeline, PipelineSlug: "other", Needs: []string{"a"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED, got %s", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "call_pipeline cannot be used inside a DAG") {
		t.Errorf("expected call_pipeline rejection message, got %q", res.ErrorMessage)
	}
}

func TestExecutor_DAG_ConditionalSkipDoesNotBlockDownstream(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_a"] = []string{"a-ok"}
	runner.outputsBySlug["agent_c"] = []string{"c-ok"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "a", Type: StepAgentRun, AgentSlug: "agent_a"},
		{ID: "b", Type: StepAgentRun, AgentSlug: "agent_b", Needs: []string{"a"},
			If: "false"},
		{ID: "c", Type: StepAgentRun, AgentSlug: "agent_c", Needs: []string{"b"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED (skipped step shouldn't fail run), got %s", res.Status)
	}
	if res.StepOutputs["b"] != "<skipped>" {
		t.Errorf("expected b skipped, got %q", res.StepOutputs["b"])
	}
	if res.StepOutputs["c"] != "c-ok" {
		t.Errorf("c should run after b's skip (DAG advances on completion regardless of skip), got %q", res.StepOutputs["c"])
	}
}

func TestExecutor_DAG_LinearModeStillWorks(t *testing.T) {
	// Sanity: a DSL with NO needs falls back to the linear path.
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["a"] = []string{"first"}
	runner.outputsBySlug["b"] = []string{"second"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "a"},
		{ID: "s2", Type: StepAgentRun, AgentSlug: "b"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %s", res.Status)
	}
	if res.StepOutputs["s1"] != "first" || res.StepOutputs["s2"] != "second" {
		t.Errorf("linear mode broken: %v", res.StepOutputs)
	}
}

// concurrentCounterRunner counts max concurrency observed during
// the run so tests can assert "at most N steps ran at once."
type concurrentCounterRunner struct {
	current atomic.Int64
	maxObs  atomic.Int64
}

func (r *concurrentCounterRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	now := r.current.Add(1)
	for {
		old := r.maxObs.Load()
		if now <= old || r.maxObs.CompareAndSwap(old, now) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	r.current.Add(-1)
	return AgentStepResult{Output: "ok", CostUSD: 0.0001}, nil
}

func TestExecutor_DAG_FanOutObservesConcurrency(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := &concurrentCounterRunner{}
	exec := NewExecutor(store, resolver, runner, nil)

	// 5 sibling steps sharing one root → all 5 should be ready at
	// once after `root` finishes.
	steps := []Step{
		{ID: "root", Type: StepAgentRun, AgentSlug: "x"},
	}
	for i := 0; i < 5; i++ {
		steps = append(steps, Step{
			ID: "leaf_" + string(rune('a'+i)), Type: StepAgentRun, AgentSlug: "x",
			Needs: []string{"root"},
		})
	}
	dsl := &DSL{Name: "x", Steps: steps}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("status: %s", res.Status)
	}
	if got := runner.maxObs.Load(); got < 5 {
		t.Errorf("expected at least 5 concurrent leaves, observed max %d", got)
	}
}
