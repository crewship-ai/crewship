package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecutor_Retry_HappyPath_NoRetry(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ok"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 1}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("status: %s", res.Status)
	}
	if got := len(runner.calls); got != 1 {
		t.Errorf("expected 1 call (no retry needed), got %d", got)
	}
}

func TestExecutor_Retry_RetriesOnError(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Two errors, then success. The mock advances errBySlug on every
	// call but outputsBySlug only when no error fires, so "finally ok"
	// is what the third (successful) call returns. Earlier this test
	// had two padding empties before the success — those used to be
	// no-ops, but the new transient-retry layer treats empty success
	// output as retryable, which the test isn't trying to exercise.
	runner.errBySlug["agent_lead"] = []error{
		errors.New("transient blip"),
		errors.New("transient blip"),
		nil,
	}
	runner.outputsBySlug["agent_lead"] = []string{"finally ok"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 1}},
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
	if got := len(runner.calls); got != 3 {
		t.Errorf("expected 3 calls (2 fails + 1 success), got %d", got)
	}
}

func TestExecutor_Retry_GivesUpAfterMaxAttempts(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// All attempts fail
	runner.errBySlug["agent_lead"] = []error{
		errors.New("blip 1"),
		errors.New("blip 2"),
		errors.New("blip 3"),
		errors.New("blip 4"),
	}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 2, InitialDelayMs: 1}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("status: %s (expected FAILED)", res.Status)
	}
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected exactly MaxAttempts=2 calls, got %d", got)
	}
}

func TestExecutor_Retry_RetryOnAllowlist(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Non-matching error → no retry
	runner.errBySlug["agent_lead"] = []error{
		errors.New("validation failed: bad input"),
	}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 1, RetryOn: []string{"timeout", "5xx"}}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("status: %s", res.Status)
	}
	if got := len(runner.calls); got != 1 {
		t.Errorf("expected 1 call (no retry on validation error), got %d", got)
	}
	if !strings.Contains(res.ErrorMessage, "validation failed") {
		t.Errorf("expected error preserved, got %q", res.ErrorMessage)
	}
}

func TestExecutor_Retry_RetryOnMatch_RetriesUntilSuccess(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	// Matching errors retry; success on 2nd attempt
	runner.errBySlug["agent_lead"] = []error{
		errors.New("Connection TIMEOUT after 30s"),
		nil,
	}
	runner.outputsBySlug["agent_lead"] = []string{"", "ok"}
	exec := NewExecutor(store, resolver, runner, nil)

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 1, RetryOn: []string{"timeout"}}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Errorf("expected COMPLETED, got %s err=%s", res.Status, res.ErrorMessage)
	}
	if got := len(runner.calls); got != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 success), got %d", got)
	}
}

func TestShouldRetry_CaseFold(t *testing.T) {
	cases := []struct {
		err     string
		allow   []string
		want    bool
		comment string
	}{
		{"connection TIMEOUT", []string{"timeout"}, true, "uppercase TIMEOUT matches lower allowlist"},
		{"got 502 bad gateway", []string{"5"}, true, "loose substring"},
		{"validation error", []string{"timeout"}, false, "no match"},
		{"anything", nil, true, "empty allowlist = retry on any"},
	}
	for _, c := range cases {
		got := shouldRetry(errors.New(c.err), c.allow)
		if got != c.want {
			t.Errorf("%s: shouldRetry(%q, %v) = %v, want %v", c.comment, c.err, c.allow, got, c.want)
		}
	}
}
