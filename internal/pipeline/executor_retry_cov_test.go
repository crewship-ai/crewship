package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// executor_retry.go — runStepWithRetry policy normalisation + backoff +
// cancellation, shouldRetry / containsCaseFold / indexCaseFold,
// runRunnerWithTransientRetry's ctx-cancel paths, sleepWithJitter,
// injectValidationFeedback, isTransientRunnerError.
// ---------------------------------------------------------------------------

// retryHarness wires a closure-runner executor plus a step carrying
// the given retry policy and returns a call counter pointer.
func retryHarness(t *testing.T, fn runnerFunc) *Executor {
	t.Helper()
	store, resolver, cleanup := openExecutorTestDB(t)
	t.Cleanup(cleanup)
	return NewExecutor(store, resolver, fn, nil)
}

func TestRunStepWithRetry_NonRetryableBreaksImmediately(t *testing.T) {
	calls := 0
	exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{}, errors.New("hard validation wall")
	})
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p",
		// MaxAttempts above the cap exercises the clamp; zero delays
		// exercise the defaults. RetryOn that never matches keeps the
		// loop from sleeping at all.
		Retry: &RetryPolicy{MaxAttempts: 20, RetryOn: []string{"", "rate limit"}}}

	_, _, _, err := exec.runStepWithRetry(context.Background(), step, "p", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeTestRun},
		"r", "p", &pipelineEmitContext{emitter: nopEmitter{}}, RenderContext{}, 0)
	if err == nil || !strings.Contains(err.Error(), "hard validation wall") {
		t.Fatalf("expected the runner error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("non-matching RetryOn must not retry, got %d calls", calls)
	}
}

func TestRunStepWithRetry_ExponentialBackoffCapsAndExhausts(t *testing.T) {
	calls := 0
	exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{}, errors.New("flaky upstream")
	})
	emitted := &captureEmitter{}
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p",
		Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 60, MaxDelayMs: 70, Backoff: "exponential"}}

	start := time.Now()
	_, _, _, err := exec.runStepWithRetry(context.Background(), step, "p", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeTestRun},
		"r", "p", &pipelineEmitContext{emitter: emitted}, RenderContext{}, 0)
	if err == nil || !strings.Contains(err.Error(), "flaky upstream") {
		t.Fatalf("expected exhausted error, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
	// Two retry journal entries (attempt 1 and 2; attempt 3 breaks).
	// Other per-attempt entries (step.failed) are emitted too, so
	// filter on the retry summary.
	retries := 0
	emitted.mu.Lock()
	for _, e := range emitted.entries {
		if strings.Contains(e.Summary, "retrying") {
			retries++
		}
	}
	emitted.mu.Unlock()
	if retries != 2 {
		t.Errorf("expected 2 retry emits, got %d", retries)
	}
	// Jittered sleeps stay below initial+capped delay.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("retry loop slept too long: %v", elapsed)
	}
}

func TestRunStepWithRetry_PreCancelledContext(t *testing.T) {
	exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		t.Fatal("runner must not be invoked with a cancelled ctx")
		return AgentStepResult{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p",
		Retry: &RetryPolicy{MaxAttempts: 2}}
	_, _, _, err := exec.runStepWithRetry(ctx, step, "p", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeTestRun},
		"r", "p", &pipelineEmitContext{emitter: nopEmitter{}}, RenderContext{}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRunStepWithRetry_CancelDuringBackoffSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		cancel() // cancel while the loop is about to sleep
		return AgentStepResult{}, errors.New("boom once")
	})
	// 40ms delay stays under the 50ms jitter threshold → deterministic
	// fixed-length sleep, raced against the already-cancelled ctx.
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p",
		Retry: &RetryPolicy{MaxAttempts: 3, InitialDelayMs: 40}}
	_, _, _, err := exec.runStepWithRetry(ctx, step, "p", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeTestRun},
		"r", "p", &pipelineEmitContext{emitter: nopEmitter{}}, RenderContext{}, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled from the sleep select, got %v", err)
	}
}

func TestShouldRetry(t *testing.T) {
	t.Parallel()
	if shouldRetry(nil, nil) {
		t.Error("nil error never retries")
	}
	if !shouldRetry(errors.New("anything"), nil) {
		t.Error("empty allowlist retries on any error")
	}
	if shouldRetry(errors.New("hard fail"), []string{"", "timeout"}) {
		t.Error("non-matching allowlist must not retry (empty entries skipped)")
	}
	if !shouldRetry(errors.New("HTTP 503 Service Unavailable"), []string{"503"}) {
		t.Error("matching substring must retry")
	}
	if !shouldRetry(errors.New("Request TIMEOUT after 30s"), []string{"timeout"}) {
		t.Error("case-folded match must retry")
	}
}

func TestCaseFoldHelpers(t *testing.T) {
	t.Parallel()
	if containsCaseFold("ab", "abc") {
		t.Error("needle longer than haystack")
	}
	if idx := indexCaseFold("anything", ""); idx != 0 {
		t.Errorf("empty needle should index 0, got %d", idx)
	}
	if idx := indexCaseFold("xxTimeOutyy", "TIMEOUT"); idx != 2 {
		t.Errorf("case-folded index: %d", idx)
	}
	if idx := indexCaseFold("nope", "timeout"); idx != -1 {
		t.Errorf("miss should be -1, got %d", idx)
	}
}

func TestRunRunnerWithTransientRetry_CtxCancelPaths(t *testing.T) {
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p"}
	emit := &pipelineEmitContext{emitter: nopEmitter{}}

	// Pre-cancelled ctx short-circuits before the first attempt.
	{
		exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
			t.Fatal("must not run")
			return AgentStepResult{}, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := exec.runRunnerWithTransientRetry(ctx, AgentStepRequest{}, step, emit); !errors.Is(err, context.Canceled) {
			t.Errorf("pre-cancelled: %v", err)
		}
	}

	// Transient error + ctx cancelled during the backoff sleep.
	{
		ctx, cancel := context.WithCancel(context.Background())
		exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
			cancel()
			return AgentStepResult{}, errors.New("429 too many requests")
		})
		if _, err := exec.runRunnerWithTransientRetry(ctx, AgentStepRequest{}, step, emit); !errors.Is(err, context.Canceled) {
			t.Errorf("transient+cancel: %v", err)
		}
	}

	// Empty output + ctx cancelled during the backoff sleep.
	{
		ctx, cancel := context.WithCancel(context.Background())
		exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
			cancel()
			return AgentStepResult{Output: "   "}, nil
		})
		if _, err := exec.runRunnerWithTransientRetry(ctx, AgentStepRequest{}, step, emit); !errors.Is(err, context.Canceled) {
			t.Errorf("empty+cancel: %v", err)
		}
	}
}

func TestSleepWithJitter_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepWithJitter(ctx, 10*time.Millisecond) {
		t.Error("cancelled ctx must return false")
	}
	if !sleepWithJitter(context.Background(), 2*time.Millisecond) {
		t.Error("live ctx must complete the sleep")
	}
}

func TestIsTransientRunnerError_Branches(t *testing.T) {
	t.Parallel()
	if isTransientRunnerError(nil) {
		t.Error("nil")
	}
	if isTransientRunnerError(context.Canceled) || isTransientRunnerError(context.DeadlineExceeded) {
		t.Error("ctx errors are not transient")
	}
	if isTransientRunnerError(errors.New("wrapped: context canceled mid flight")) {
		t.Error("ctx error text is not transient")
	}
	if isTransientRunnerError(errors.New("wrapped: context deadline blew past")) {
		t.Error("ctx deadline text is not transient")
	}
	if !isTransientRunnerError(errors.New("upstream returned 502 Bad Gateway")) {
		t.Error("5xx is transient")
	}
	if isTransientRunnerError(errors.New("schema mismatch")) {
		t.Error("plain failures are not transient")
	}
}

func TestInjectValidationFeedback(t *testing.T) {
	t.Parallel()

	// Empty/whitespace reason → prompt unchanged.
	if got := injectValidationFeedback("do the thing", "   "); got != "do the thing" {
		t.Errorf("empty reason: %q", got)
	}

	// Normal reason is prepended with the fence and keeps the prompt.
	got := injectValidationFeedback("do the thing", "output was too short")
	if !strings.HasPrefix(got, "[PREVIOUS ATTEMPT FAILED VALIDATION: output was too short") {
		t.Errorf("prefix: %q", got)
	}
	if !strings.HasSuffix(got, "do the thing") {
		t.Errorf("original prompt lost: %q", got)
	}

	// Over-long reason truncates on a rune boundary with ellipsis.
	long := strings.Repeat("a", 596) + "世界"
	got = injectValidationFeedback("p", long)
	if !strings.Contains(got, "…") {
		t.Errorf("expected ellipsis in truncated feedback")
	}
	if strings.Contains(got, "�") {
		t.Error("truncation produced invalid UTF-8")
	}
}
