package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// Per-step retry policy (#861): the new backoff{min_ms,max_ms,factor,jitter}
// + CEL retry_on shape, driven by a fake clock so the backoff schedule is
// asserted deterministically (no real time.Sleep in the test).

// sleepRec is a fake retry clock: it records each requested delay and
// returns at once, honouring cancellation.
type sleepRec struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleepRec) sleep(ctx context.Context, d time.Duration) bool {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err() == nil
}

func (s *sleepRec) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.delays))
	copy(out, s.delays)
	return out
}

func countRetryEmits(e *captureEmitter) (total int, sawMax3 bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, entry := range e.entries {
		if strings.Contains(entry.Summary, "retrying") {
			total++
			if strings.Contains(entry.Summary, "1/3") || strings.Contains(entry.Summary, "2/3") {
				sawMax3 = true
			}
		}
	}
	return total, sawMax3
}

// 429 → 429 → success: the step policy retries the transient failure and
// the run COMPLETES, with the recovery visible in the trace (attempt N/M).
// A non-transient-marker error is used so ONLY the step policy retries
// (the inner same-tier transient layer stays out of it), keeping the call
// count unambiguous.
func TestRetryPolicy_RetriesTransientUntilSuccess(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["worker"] = []error{
		errors.New("provider quota exceeded, back off"),
		errors.New("provider quota exceeded, back off"),
		nil,
	}
	runner.outputsBySlug["worker"] = []string{"done"}
	emitted := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, emitted)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, RetryOn: `error.contains("quota")`}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "COMPLETED" {
		t.Fatalf("expected COMPLETED, got %s err=%s", res.Status, res.ErrorMessage)
	}
	if got := len(runner.calls); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
	total, sawMax3 := countRetryEmits(emitted)
	if total != 2 {
		t.Errorf("expected 2 retry emits (attempts 1 and 2), got %d", total)
	}
	if !sawMax3 {
		t.Errorf("expected a retry emit tagged attempt N/3 (the step policy's max) in the trace")
	}
}

// retry_on that doesn't match the error = immediate abort: no retry, one
// attempt, the original error preserved.
func TestRetryPolicy_RetryOnMismatch_AbortsImmediately(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["worker"] = []error{
		errors.New("bad request: malformed field"),
		nil, // never reached
	}
	runner.outputsBySlug["worker"] = []string{"unused"}
	exec := NewExecutor(store, resolver, runner, nil)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 5, RetryOn: `error.contains("quota")`}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED, got %s", res.Status)
	}
	if got := len(runner.calls); got != 1 {
		t.Errorf("retry_on mismatch must not retry: got %d calls", got)
	}
	if !strings.Contains(res.ErrorMessage, "malformed field") {
		t.Errorf("original error must be preserved, got %q", res.ErrorMessage)
	}
}

// Composition with the inner same-tier transient retry: an explicit step
// retry policy OWNS transient retries, so the inner loop stands down. A
// transient 429 on every attempt therefore costs exactly MaxAttempts provider
// calls — not MaxAttempts × 2. Also exercises the `status` CEL variable
// end-to-end (retry_on: "status == 429").
func TestRetryPolicy_ExplicitPolicyDisablesInnerTransientRetry(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	var calls int
	fn := runnerFunc(func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{}, errors.New("upstream returned HTTP 429 too many requests")
	})
	exec := NewExecutor(store, resolver, fn, nil)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 3, RetryOn: "status == 429"}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED after exhausting attempts, got %s", res.Status)
	}
	// 3 step attempts × 1 provider call each (inner transient loop stood down)
	// = 3. Without the composition guard this would be 6 (each attempt's inner
	// loop retrying the 429 once more).
	if calls != 3 {
		t.Errorf("explicit retry policy must bound provider calls to MaxAttempts: got %d, want 3", calls)
	}
}

// max_cost_usd trips MID-retry, PREDICTIVELY: each attempt costs money
// (execution succeeds, validation gate rejects it), so the retry loop stops
// BEFORE the attempt that would breach the cap — and wraps the real failure
// rather than masking it.
func TestRetryPolicy_CostCapEndsRetryMidLoop(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	// Each execution succeeds (and costs $1) but fails the min-length gate,
	// so runStep returns an error carrying the attempt's cost — exactly the
	// case where the retry loop must watch the budget.
	var calls int
	fn := runnerFunc(func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{Output: "short", CostUSD: 1.0}, nil
	})
	exec := NewExecutor(store, resolver, fn, nil)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", MaxCostUSD: 2.5, Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Validation: &Validation{MinLength: intPtr(100)},
			Retry:      &RetryPolicy{MaxAttempts: 5}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("expected FAILED, got %s", res.Status)
	}
	if !strings.Contains(res.ErrorMessage, "cost cap") {
		t.Errorf("expected a cost-cap failure, got %q", res.ErrorMessage)
	}
	// The real failure must be WRAPPED, not masked, so an operator still sees
	// WHY the step was failing.
	if !strings.Contains(res.ErrorMessage, "below min 100") {
		t.Errorf("cost-cap error must wrap the underlying failure, got %q", res.ErrorMessage)
	}
	// $1/attempt, cap $2.5: after attempt 2 (spent $2.0) the next (~$1.0) would
	// breach, so the loop stops PREDICTIVELY at 2 — before overspending, and
	// well short of MaxAttempts=5.
	if calls != 2 {
		t.Errorf("predictive cost cap must stop before the breaching attempt: got %d, want 2", calls)
	}
}

// Backoff is bounded (grows by factor, clamped at max_ms) and jittered
// (the injected jitter hook shapes each actual sleep). Asserted with a
// fake clock so the exact schedule is deterministic.
func TestRetryPolicy_BackoffBoundedAndJittered(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	runner := newMockRunner()
	runner.errBySlug["worker"] = []error{
		errors.New("wobble"), errors.New("wobble"), errors.New("wobble"),
		errors.New("wobble"), errors.New("wobble"),
	}
	exec := NewExecutor(store, resolver, runner, nil)
	rec := &sleepRec{}
	exec.sleepFn = rec.sleep
	// Deterministic jitter: halve every base delay, so a recorded value of
	// base/2 proves the jitter hook shaped the sleep.
	exec.jitterFn = func(d time.Duration) time.Duration { return d / 2 }

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 5, RetryOn: `error.contains("wobble")`,
				Backoff: &BackoffPolicy{MinMs: 100, MaxMs: 400, Factor: 2, Jitter: boolPtr(true)}}},
	}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "FAILED" {
		t.Errorf("expected FAILED after exhausting attempts, got %s", res.Status)
	}
	// 5 attempts → 4 sleeps. Base schedule 100,200,400,400 (capped at 400);
	// jitter halves each → 50,100,200,200.
	got := rec.recorded()
	want := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}
	if len(got) != len(want) {
		t.Fatalf("expected %d backoff sleeps, got %d: %v", len(want), len(got), got)
	}
	maxMs := 400 * time.Millisecond
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v (bounded+jittered schedule)", i, got[i], want[i])
		}
		if got[i] > maxMs {
			t.Errorf("sleep[%d] = %v exceeds max_ms bound %v", i, got[i], maxMs)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// The retry_on CEL classifier: bool predicates over error/transient,
// empty = always retry, non-bool = a compile error.
func TestRetryOnClassifier(t *testing.T) {
	// Empty expression compiles to a nil program = retry any error.
	prg, err := compileRetryOn("")
	if err != nil || prg != nil {
		t.Fatalf("empty retry_on: prg=%v err=%v", prg, err)
	}
	if !evalRetryOn(nil, errors.New("anything")) {
		t.Error("nil program must retry any error")
	}
	if evalRetryOn(nil, nil) {
		t.Error("nil error never retries")
	}

	// A bool predicate over the error message.
	prg, err = compileRetryOn(`error.contains("429")`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !evalRetryOn(prg, errors.New("HTTP 429 rate limited")) {
		t.Error("429 should match")
	}
	if evalRetryOn(prg, errors.New("400 bad request")) {
		t.Error("400 should not match")
	}

	// The `transient` shorthand tracks the built-in classifier.
	prg, err = compileRetryOn("transient")
	if err != nil {
		t.Fatalf("compile transient: %v", err)
	}
	if !evalRetryOn(prg, errors.New("upstream 503 service unavailable")) {
		t.Error("503 is transient")
	}
	if evalRetryOn(prg, errors.New("schema mismatch")) {
		t.Error("schema mismatch is not transient")
	}

	// A non-bool expression is rejected at compile (caught at save time).
	if _, err := compileRetryOn("error"); err == nil {
		t.Error("a string-typed retry_on must fail to compile (needs bool)")
	}
	// A syntactically invalid expression is rejected too.
	if _, err := compileRetryOn("error.("); err == nil {
		t.Error("invalid CEL must fail to compile")
	}
}

func TestExtractHTTPStatus(t *testing.T) {
	cases := map[string]int{
		`http step "x" got HTTP 429 (success codes: [200])`: 429,
		"upstream returned HTTP 503 service unavailable":    503,
		// Only the explicit "HTTP <code>" shape counts — a bare 3-digit
		// token must NOT be misread as a status (request ids, timings, etc.).
		"503 Service Unavailable": 0,
		"connection refused":      0,
		"took 250ms":              0,
		"request 500 of 999":      0,
		"":                        0,
	}
	for msg, want := range cases {
		if got := extractHTTPStatus(msg); got != want {
			t.Errorf("extractHTTPStatus(%q) = %d, want %d", msg, got, want)
		}
	}
}

// A retry: {max_attempts: 1} policy retries nothing, so it must NOT disable
// the inner same-tier transient retry floor — otherwise an inert retry block
// would leave the step with FEWER retries than a plain step. A transient 429
// on the first call should still be reissued once on the same tier (2 calls).
func TestRetryPolicy_MaxAttemptsOne_KeepsTransientFloor(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	var calls int
	fn := runnerFunc(func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{}, errors.New("upstream returned HTTP 429 too many requests")
	})
	exec := NewExecutor(store, resolver, fn, nil)
	exec.sleepFn = instantSleep

	dsl := &DSL{Name: "x", Steps: []Step{
		{ID: "s1", Type: StepAgentRun, AgentSlug: "worker", Prompt: "go",
			Retry: &RetryPolicy{MaxAttempts: 1}},
	}}
	if _, err := exec.RunDefinition(context.Background(), dsl, RunInput{
		WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	// max_attempts:1 → no step-level retry, but the inner transient floor
	// still reissues the 429 once = 2 provider calls (not 1).
	if calls != retryAttemptsPerTier {
		t.Errorf("max_attempts:1 must keep the transient floor: got %d calls, want %d", calls, retryAttemptsPerTier)
	}
}

func TestRetryOn_StatusVariable(t *testing.T) {
	prg, err := compileRetryOn("status >= 500")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if !evalRetryOn(prg, errors.New(`http step "x" got HTTP 503`)) {
		t.Error("503 should match status >= 500")
	}
	if evalRetryOn(prg, errors.New(`http step "x" got HTTP 404`)) {
		t.Error("404 should not match status >= 500")
	}
}

// A retry_on that fails to compile at run time (a stored-row anomaly that
// slipped past save-time validation) must FAIL SAFE — no retry — rather than
// degrade to retry-ANY. Constructed by calling runStepWithRetry directly with
// an invalid predicate (Validate would normally reject it).
func TestRunStepWithRetry_BrokenRetryOnDoesNotRetry(t *testing.T) {
	calls := 0
	exec := retryHarness(t, func(_ context.Context, _ AgentStepRequest) (AgentStepResult, error) {
		calls++
		return AgentStepResult{}, errors.New("some transient 503")
	})
	exec.sleepFn = instantSleep
	step := Step{ID: "s", Type: StepAgentRun, AgentSlug: "a", Prompt: "p",
		Retry: &RetryPolicy{MaxAttempts: 5, RetryOn: "this is not ( valid cel"}}
	_, _, _, err := exec.runStepWithRetry(context.Background(), step, "p", AdapterModel{}, nil,
		RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun},
		"r", "p", &pipelineEmitContext{emitter: nopEmitter{}}, RenderContext{}, 0, 0)
	if err == nil {
		t.Fatal("expected the runner error to surface")
	}
	if calls != 1 {
		t.Errorf("a broken retry_on must not retry (fail safe): got %d calls, want 1", calls)
	}
}

func TestValidateRetryPolicy_MaxAttemptsCeiling(t *testing.T) {
	// > ceiling is rejected loudly (matches schema maximum:10), not clamped.
	err := validateRetryPolicy(Step{ID: "s", Retry: &RetryPolicy{MaxAttempts: 50}})
	if err == nil || !strings.Contains(err.Error(), "must be <= 10") {
		t.Errorf("max_attempts=50 must be rejected, got %v", err)
	}
	// The ceiling itself is fine.
	if err := validateRetryPolicy(Step{ID: "s", Retry: &RetryPolicy{MaxAttempts: 10}}); err != nil {
		t.Errorf("max_attempts=10 must be accepted, got %v", err)
	}
}
