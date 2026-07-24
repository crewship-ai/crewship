package pipeline

import (
	"context"
	"strings"
	"testing"
)

// costedRunner returns a fixed cost per call for the named slug so cost-cap
// tests can drive precise budgets (the default mockRunner returns 0.001).
type costedRunner struct {
	cost   float64
	mu     chan struct{}
	bySlug map[string]int // call counts
}

func newCostedRunner(cost float64) *costedRunner {
	return &costedRunner{cost: cost, bySlug: map[string]int{}, mu: make(chan struct{}, 1)}
}

func (c *costedRunner) RunStep(_ context.Context, req AgentStepRequest) (AgentStepResult, error) {
	c.mu <- struct{}{}
	c.bySlug[req.AgentSlug]++
	<-c.mu
	return AgentStepResult{Output: "out-" + req.AgentSlug, CostUSD: c.cost, DurationMs: 5}, nil
}

func (c *costedRunner) calls(slug string) int {
	c.mu <- struct{}{}
	defer func() { <-c.mu }()
	return c.bySlug[slug]
}

// #1427, 2.3a — a B→A / A→B cycle created in the wrong order is caught at
// SAVE time once the resolver is draft-aware. Persisted A calls B; the draft
// B now calls A. Without DraftAwareResolver the back-edge (B) resolves to the
// stale persisted B and the cycle is missed.
func TestCycleDetect_DraftAware_BackEdge(t *testing.T) {
	// A is already persisted and calls B. When A was saved, B did not exist,
	// so A's call to B resolved to "unknown" and CycleDetect saved it clean.
	aDSL := &DSL{
		Name: "a",
		Steps: []Step{
			{ID: "callB", Type: StepCallPipeline, PipelineSlug: "b"},
		},
	}
	// B is being saved for the FIRST time — it is not yet in the registry,
	// so the plain resolver returns "unknown" for its own slug. This is the
	// exact wrong-order case: A→(unknown B) saved first, now B→A.
	inner := func(slug string) (*DSL, error) {
		if slug == "a" {
			return aDSL, nil
		}
		return nil, ErrNotFound
	}

	// draftB is the version being saved: it now calls A → completes the cycle.
	draftB := &DSL{
		Name: "b",
		Steps: []Step{
			{ID: "callA", Type: StepCallPipeline, PipelineSlug: "a"},
		},
	}

	// Baseline: the plain resolver misses the cycle (documents the defect).
	if err := CycleDetect(draftB, inner); err != nil {
		t.Fatalf("plain resolver unexpectedly detected the cycle: %v", err)
	}

	// Fixed: the draft-aware resolver feeds draftB back for slug "b".
	err := CycleDetect(draftB, DraftAwareResolver("b", draftB, inner))
	if err == nil {
		t.Fatalf("expected cycle to be detected at save time, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected a cycle error, got %v", err)
	}
}

// #1427, 2.3b — a runtime A↔B cycle is rejected before it churns to the depth
// ceiling. Both routines exist and call each other; running A must fail fast
// with ErrRuntimeCycleDetected rather than ErrMaxDepthExceeded.
func TestExecutor_RuntimeCycle_Rejected(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)

	a := fakePipeline(t, "cyc-a",
		`{"dsl_version":"1.0","name":"cyc-a","steps":[{"id":"cb","type":"call_pipeline","pipeline_slug":"cyc-b"}]}`,
		"crew_a", "agent_lead")
	b := fakePipeline(t, "cyc-b",
		`{"dsl_version":"1.0","name":"cyc-b","steps":[{"id":"ca","type":"call_pipeline","pipeline_slug":"cyc-a"}]}`,
		"crew_a", "agent_lead")
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		switch slug {
		case "cyc-a":
			return a, nil
		case "cyc-b":
			return b, nil
		}
		return nil, ErrNotFound
	}))

	// Run A via RunDefinition (top DSL supplied directly); the child lookups
	// go through the resolver above. A→B→A must trip the runtime guard.
	aParsed, perr := Parse([]byte(a.DefinitionJSON))
	if perr != nil {
		t.Fatal(perr)
	}
	res, err := exec.RunDefinition(context.Background(), aParsed, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED on runtime cycle, got %s (%s)", res.Status, res.ErrorMessage)
	}
	if !strings.Contains(res.ErrorMessage, "cycle") {
		t.Errorf("expected a runtime cycle message, got %q", res.ErrorMessage)
	}
	if strings.Contains(res.ErrorMessage, "max nested depth") {
		t.Errorf("cycle should be caught before the depth ceiling, got %q", res.ErrorMessage)
	}
}

// #1427, 2.4 — a parent's budget bounds the call_pipeline child's spend. The
// child inherits the parent's REMAINING budget and stops mid-way instead of
// counting from zero against only its own (unset) cap.
func TestExecutor_ParentBudget_BoundsChild(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newCostedRunner(0.03)
	exec := NewExecutor(store, resolver, runner, nil)

	// Child C: no own cap, two agent steps (each $0.03).
	child := fakePipeline(t, "child-c",
		`{"dsl_version":"1.0","name":"child-c","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"},`+
			`{"id":"c2","type":"agent_run","agent_slug":"cagent","prompt":"2"}]}`,
		"crew_a", "agent_lead")
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "child-c" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	// Parent P: cap $0.05, agent step a1 ($0.03) then call child-c.
	parent := &DSL{
		Name:       "parent-p",
		MaxCostUSD: 0.05,
		Steps: []Step{
			{ID: "a1", Type: StepAgentRun, AgentSlug: "pagent", Prompt: "p"},
			{ID: "callC", Type: StepCallPipeline, PipelineSlug: "child-c"},
		},
	}
	res, err := exec.RunDefinition(context.Background(), parent, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED (budget bounded child), got %s", res.Status)
	}
	// Parent remaining when C started: 0.05-0.03 = 0.02. c1 ($0.03) trips
	// C's inherited cap → c2 must never run.
	if got := runner.calls("cagent"); got != 1 {
		t.Errorf("child should stop after 1 step (parent budget), ran %d", got)
	}
}

// #1427, 3.7/3.8 — buildNestedRunInput propagates tier + user and stamps
// parentage / budget / call path onto the child RunInput.
func TestBuildNestedRunInput_Propagation(t *testing.T) {
	parent := RunInput{
		WorkspaceID:     "ws",
		AuthorCrewID:    "crew_parent",
		AuthorAgentID:   "agent_parent",
		InvokingUserID:  "user_42",
		TierOverride:    ComplexityFast,
		Mode:            ModeRun,
		remainingBudget: 0, // top-level
	}
	target := &Pipeline{AuthorCrewID: "crew_child", AuthorAgentID: "agent_child", Slug: "child"}
	dsl := &DSL{Name: "child"}
	child := buildNestedRunInput(parent, target, dsl, map[string]any{"k": "v"},
		"run_parent", 0.02, []string{"parent-p"})

	if child.InvokingUserID != "user_42" {
		t.Errorf("InvokingUserID not propagated: %q", child.InvokingUserID)
	}
	if child.TierOverride != ComplexityFast {
		t.Errorf("TierOverride not propagated: %q", child.TierOverride)
	}
	if child.TriggeredVia != TriggeredViaCallPipeline {
		t.Errorf("TriggeredVia = %q, want call_pipeline", child.TriggeredVia)
	}
	if child.TriggeredByID != "run_parent" {
		t.Errorf("TriggeredByID = %q, want run_parent", child.TriggeredByID)
	}
	if child.remainingBudget != 0.02 {
		t.Errorf("remainingBudget = %v, want 0.02", child.remainingBudget)
	}
	if child.AuthorCrewID != "crew_child" {
		t.Errorf("child must run in target's author crew, got %q", child.AuthorCrewID)
	}
	if child.InvokingCrewID != "crew_parent" {
		t.Errorf("child invoker must be parent author crew, got %q", child.InvokingCrewID)
	}
	if len(child.callPath) != 1 || child.callPath[0] != "parent-p" {
		t.Errorf("callPath = %v, want [parent-p]", child.callPath)
	}
}
