package pipeline

import (
	"context"
	"testing"
)

// #1429 (2.10) — a step's per-step retry loop must NOT retry a tiers-exhausted
// validation/outcomes failure when retry_on is absent. Otherwise a
// deterministic validation miss multiplies (max_attempts × tiers) worker +
// grader LLM calls. The transient-execution retry the loop exists for is
// unaffected; only the terminal tiers-exhausted outcome stands down by
// default, and an explicit retry_on may still opt it back in.
func TestExecutor_Retry_DoesNotCompoundTiersExhausted(t *testing.T) {
	workerCalls := func(r *mockRunner) int {
		n := 0
		for _, c := range r.calls {
			if c.AgentSlug == "worker" {
				n++
			}
		}
		return n
	}

	run := func(t *testing.T, rp *RetryPolicy) (*RunResult, int) {
		t.Helper()
		store, resolver, cleanup := openExecutorTestDB(t)
		defer cleanup()
		seedTierFallback(t, store) // primary + one fallback tier => 2 tiers
		runner := newMockRunner()
		exec := NewExecutor(store, resolver, runner, nil)
		exec.sleepFn = instantSleep

		dsl := &DSL{Name: "x", Steps: []Step{
			{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
				OnFail:     OnFailEscalateTier,
				Validation: &Validation{MinLength: intPtr(500)}, // never satisfied
				Retry:      rp},
		}}
		res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
			WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		return res, workerCalls(runner)
	}

	t.Run("no retry_on -> no compounding", func(t *testing.T) {
		res, calls := run(t, &RetryPolicy{MaxAttempts: 3})
		if res.Status != "FAILED" {
			t.Fatalf("status: %+v", res)
		}
		// 2 tiers, exactly one pass (no retry loop re-runs).
		if calls != 2 {
			t.Errorf("worker calls = %d, want 2 (retry must not multiply a tiers-exhausted failure)", calls)
		}
	})

	t.Run("explicit retry_on -> opts back in", func(t *testing.T) {
		res, calls := run(t, &RetryPolicy{MaxAttempts: 3, RetryOn: `error.contains("exhausting tiers")`})
		if res.Status != "FAILED" {
			t.Fatalf("status: %+v", res)
		}
		// 3 attempts × 2 tiers = 6 worker calls when the author opts in.
		if calls != 6 {
			t.Errorf("worker calls = %d, want 6 (explicit retry_on must still retry)", calls)
		}
	})
}
