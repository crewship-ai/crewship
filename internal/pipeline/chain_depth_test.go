package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The cap is a NUMBER, and a number nobody pins drifts. 8 is the documented
// ceiling; 9 is the first refusal. Both directions are asserted so a change to
// MaxChainDepth has to change this test deliberately rather than silently.
func TestGuardChainDepth_Boundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		next    int
		refused bool
	}{
		{"root", 0, false},
		{"one hop", 1, false},
		{"at the cap", MaxChainDepth, false},
		{"one past the cap", MaxChainDepth + 1, true},
		{"far past the cap", MaxChainDepth + 40, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := GuardChainDepth(tc.next)
			if tc.refused && err == nil {
				t.Fatalf("GuardChainDepth(%d) = nil, want refusal", tc.next)
			}
			if !tc.refused && err != nil {
				t.Fatalf("GuardChainDepth(%d) = %v, want nil", tc.next, err)
			}
			if tc.refused && !errors.Is(err, ErrChainDepthExceeded) {
				t.Fatalf("GuardChainDepth(%d) error does not wrap ErrChainDepthExceeded: %v", tc.next, err)
			}
		})
	}
	if MaxChainDepth != 8 {
		t.Fatalf("MaxChainDepth = %d, want 8 — raising the cap is allowed, "+
			"lowering it breaks chains people already built; change this line deliberately",
			MaxChainDepth)
	}
}

// Depth 9 is refused. A run already 8 composed hops deep asks for one more
// through call_pipeline: the run must FAIL rather than dispatch, and the
// refusal must be visible in the journal as automation.depth_exceeded — a cap
// that refuses silently is a cap nobody can debug.
func TestExecutor_ChainDepth_NinthHopRefused(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	emitter := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, emitter)

	child := fakePipeline(t, "deep-child",
		`{"dsl_version":"1.0","name":"deep-child","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"}]}`,
		"crew_a", "agent_lead")
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "deep-child" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	parent := &DSL{
		Name:  "deep-parent",
		Steps: []Step{{ID: "callChild", Type: StepCallPipeline, PipelineSlug: "deep-child"}},
	}
	res, err := exec.RunDefinition(context.Background(), parent, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		// Already at the cap: the edge this step wants is hop 9.
		ChainDepth:  MaxChainDepth,
		ChainOrigin: "run_origin",
	})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED at the depth cap, got %s (%s)", res.Status, res.ErrorMessage)
	}
	if !strings.Contains(res.ErrorMessage, "chain depth") {
		t.Errorf("expected a chain-depth message, got %q", res.ErrorMessage)
	}
	// The child must never have been dispatched — refusing AFTER running the
	// nested routine would defeat the whole point of a budget.
	if got := runner.calls; got != nil && len(got) > 0 {
		t.Errorf("child pipeline ran despite the depth refusal: %v", got)
	}

	var depthEntry *journal.Entry
	for i := range emitter.entries {
		if emitter.entries[i].Type == journal.EntryAutomationDepthExceeded {
			depthEntry = &emitter.entries[i]
			break
		}
	}
	if depthEntry == nil {
		t.Fatalf("no automation.depth_exceeded entry emitted; got %v", emitter.typesEmitted())
	}
	if got, ok := depthEntry.Payload["chain_depth"].(int); !ok || got != MaxChainDepth+1 {
		t.Errorf("chain_depth payload = %v, want %d", depthEntry.Payload["chain_depth"], MaxChainDepth+1)
	}
	if got, _ := depthEntry.Payload["chain_origin"].(string); got != "run_origin" {
		t.Errorf("chain_origin payload = %q, want run_origin (the inherited origin, not a re-root)", got)
	}
	if depthEntry.Severity != journal.SeverityError {
		t.Errorf("severity = %q, want error", depthEntry.Severity)
	}
}

// A hop UNDER the cap still runs. The guard must bound composition, not
// forbid it — without this, "refuse everything" would pass the test above.
func TestExecutor_ChainDepth_UnderCapStillRuns(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	exec := NewExecutor(store, resolver, runner, nil)

	child := fakePipeline(t, "ok-child",
		`{"dsl_version":"1.0","name":"ok-child","steps":[`+
			`{"id":"c1","type":"agent_run","agent_slug":"cagent","prompt":"1"}]}`,
		"crew_a", "agent_lead")
	exec.WithPipelineResolver(pipeResolverFn(func(_ context.Context, _, slug string) (*Pipeline, error) {
		if slug == "ok-child" {
			return child, nil
		}
		return nil, ErrNotFound
	}))

	parent := &DSL{
		Name:  "ok-parent",
		Steps: []Step{{ID: "callChild", Type: StepCallPipeline, PipelineSlug: "ok-child"}},
	}
	res, err := exec.RunDefinition(context.Background(), parent, RunInput{
		WorkspaceID:  "ws_test",
		AuthorCrewID: "crew_a",
		Mode:         ModeRun,
		ChainDepth:   MaxChainDepth - 1, // the edge is hop 8 — the last allowed one
	})
	if err != nil {
		t.Fatalf("Run returned transport error: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED one hop under the cap, got %s (%s)", res.Status, res.ErrorMessage)
	}
}

// The budget is only a budget if the child SPENDS from it. A child that
// inherited depth 0 would make every chain infinite one call_pipeline at a
// time, which is the exact failure the column exists to prevent.
func TestBuildNestedRunInput_ChainDepthAndOrigin(t *testing.T) {
	target := &Pipeline{AuthorCrewID: "crew_child", AuthorAgentID: "agent_child", Slug: "child"}
	dsl := &DSL{Name: "child"}

	t.Run("root parent seeds the origin from its own run id", func(t *testing.T) {
		parent := RunInput{WorkspaceID: "ws", Mode: ModeRun, ChainDepth: 0}
		child := buildNestedRunInput(parent, target, dsl, nil, "run_parent", 0, nil, 1)
		if child.ChainDepth != 1 {
			t.Errorf("ChainDepth = %d, want 1", child.ChainDepth)
		}
		if child.ChainOrigin != "run_parent" {
			t.Errorf("ChainOrigin = %q, want run_parent", child.ChainOrigin)
		}
	})

	t.Run("mid-chain parent passes its origin through unchanged", func(t *testing.T) {
		parent := RunInput{WorkspaceID: "ws", Mode: ModeRun, ChainDepth: 3, ChainOrigin: "entry_root"}
		child := buildNestedRunInput(parent, target, dsl, nil, "run_parent", 0, nil, 4)
		if child.ChainDepth != 4 {
			t.Errorf("ChainDepth = %d, want 4", child.ChainDepth)
		}
		// A re-rooted chain reads as two short chains — which is exactly what
		// a loop would like to look like.
		if child.ChainOrigin != "entry_root" {
			t.Errorf("ChainOrigin = %q, want entry_root (no re-rooting mid-chain)", child.ChainOrigin)
		}
	})
}
