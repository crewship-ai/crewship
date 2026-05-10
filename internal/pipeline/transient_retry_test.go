package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Same-tier transient retry sits BETWEEN the tier-escalation chain
// and the explicit step.Retry policy: a Haiku call that 429s should
// get a second shot on Haiku before we burn an Opus call. These
// tests pin the boundaries so a future refactor doesn't accidentally
// undo the savings.

func TestRunAgentStep_RetriesEmptyOutputOnSameTier(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// First call returns success but empty (transient-class). Second
	// call returns a real answer. No fallback tiers — both attempts
	// are on the same primary tier.
	runner.outputsBySlug["agent_lead"] = []string{"", "real answer"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %s err=%s", res.Status, res.ErrorMessage)
	}
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected 2 calls (1 empty + 1 real), got %d", got)
	}
	if got := res.Output; got != "real answer" {
		t.Errorf("expected output %q, got %q", "real answer", got)
	}
}

func TestRunAgentStep_RetriesTransientErrorOnSameTier(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Real-world rate-limit error message — should match the
	// transient marker list and trigger an inner retry. Outputs only
	// advance on the successful call.
	runner.errBySlug["agent_lead"] = []error{
		errors.New("anthropic 429: rate limit exceeded"),
		nil,
	}
	runner.outputsBySlug["agent_lead"] = []string{"recovered"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go"},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %s err=%s", res.Status, res.ErrorMessage)
	}
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected 2 calls (1 transient + 1 success), got %d", got)
	}
}

func TestRunAgentStep_DoesNotRetryNonTransientError(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// "permission denied" isn't on the transient marker list — the
	// step should escalate (or in this single-tier setup, fail) on
	// the first attempt rather than retry.
	runner.errBySlug["agent_lead"] = []error{
		errors.New("permission denied: secret not provisioned"),
	}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go"},
	}}
	res, _ := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	// runDAG persists the failure on the result rather than bubbling
	// the error, so we assert on Status instead of the returned err.
	if res.Status == "COMPLETED" {
		t.Fatalf("expected non-COMPLETED status, got %q", res.Status)
	}
	if got := len(runner.calls); got != 1 {
		t.Errorf("expected 1 call (no retry on non-transient), got %d", got)
	}
}

func TestIsTransientRunnerError_Matrix(t *testing.T) {
	cases := map[string]bool{
		"":                                          false,
		"permission denied":                         false,
		"context canceled":                          false,
		"context deadline exceeded":                 false,
		"anthropic 429: rate limit exceeded":        true,
		"upstream 503 service unavailable":          true,
		"i/o timeout":                               true,
		"connection refused":                        true,
		"broken pipe":                               true,
		"500 internal server error":                 true,
		"too many requests":                         true,
		"429 Too Many Requests (slow down)":         true,
		"random unrelated error like ENOMEM mapped": false,
	}
	for msg, want := range cases {
		var err error
		if msg == "" {
			err = nil
		} else {
			err = errors.New(msg)
		}
		got := isTransientRunnerError(err)
		if got != want {
			t.Errorf("isTransientRunnerError(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestInjectValidationFeedback_Format(t *testing.T) {
	// First-attempt prompts pass through untouched.
	if got := injectValidationFeedback("ask the model", ""); got != "ask the model" {
		t.Errorf("empty reason should pass prompt through unchanged, got %q", got)
	}
	out := injectValidationFeedback("ask the model", "missing required key 'k'")
	if !strings.Contains(out, "PREVIOUS ATTEMPT FAILED VALIDATION") {
		t.Errorf("feedback header missing: %q", out)
	}
	if !strings.Contains(out, "missing required key 'k'") {
		t.Errorf("reason missing from prompt: %q", out)
	}
	if !strings.HasSuffix(out, "ask the model") {
		t.Errorf("original prompt should be at the end, got %q", out)
	}
}

func TestInjectValidationFeedback_TruncatesLongReason(t *testing.T) {
	long := strings.Repeat("x", 2000)
	out := injectValidationFeedback("p", long)
	// Header + reason (capped) + tail prompt should be well under 2000 chars.
	if len(out) > 1200 {
		t.Errorf("reason should be truncated to keep prompt small, got len=%d", len(out))
	}
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis on truncated reason: %q", out)
	}
}
