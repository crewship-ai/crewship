package pipeline

import (
	"context"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"time"
)

// runStepWithRetry wraps runStep with the per-step retry policy.
// Distinct concern from OnFail (which handles validation failure):
// retry covers EXECUTION error — the step's runner returned an
// error before we could even validate. HTTP 5xx, code timeout,
// network blip, transient agent crash all fit here.
//
// Order of operations on failure:
//  1. The step's underlying runner errors (HTTP 5xx, etc.)
//  2. retry policy decides: retry-and-sleep, or surface
//  3. If retries exhausted (or no policy), return error to caller
//  4. Caller (runDSL) marks run FAILED
//
// We don't retry on context cancellation — ctx.Err() short-circuits
// out so a Cancel takes effect immediately rather than sleeping
// through the backoff.
func (e *Executor) runStepWithRetry(
	ctx context.Context,
	step Step,
	renderedPrompt string,
	primary AdapterModel,
	fallback []AdapterModel,
	in RunInput,
	runID, pipelineID string,
	emit *pipelineEmitContext,
	parentRender RenderContext,
	depth int,
	priorCostUSD float64,
) (string, float64, int64, error) {
	// on_fail: retry_step with no explicit retry: block is sugar for the
	// default policy (desugared at the chokepoint, no separate layer).
	rp := step.Retry
	if rp == nil && step.OnFail == OnFailRetryStep {
		rp = defaultRetryPolicy()
	}
	if rp == nil || rp.MaxAttempts <= 1 {
		return e.runStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth)
	}

	maxAttempts := rp.MaxAttempts
	if maxAttempts > retryMaxAttemptsCeiling {
		// Cap to keep a runaway retry from monopolising the run
		// budget. 10 attempts is the conventional ceiling.
		maxAttempts = retryMaxAttemptsCeiling
	}
	minDelay, maxDelay, factor, jitter := resolveBackoff(rp.Backoff)

	// Compile the retry_on predicate once (nil program = retry any error).
	// It is validated at save time, so a compile failure here is a
	// stored-row anomaly. Fail SAFE: do NOT retry (a broken predicate must
	// not silently degrade to retry-ANY, which could hammer an upstream on
	// an error the author never meant to retry).
	classifier, retryOnBroken := compileRetryOn(rp.RetryOn)
	if retryOnBroken != nil {
		classifier = nil
	}

	// Cost ceiling: reading in.dsl keeps the retry loop honest about the
	// run-level budget so it stops mid-retry instead of overrunning it.
	var maxCost float64
	if in.dsl != nil {
		maxCost = in.dsl.MaxCostUSD
	}

	var (
		lastOut string
		lastDur int64
		lastErr error
		costSum float64
	)
	delay := minDelay
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", costSum, 0, err
		}
		out, c, dur, err := e.runStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth)
		costSum += c
		if err == nil {
			return out, costSum, dur, nil
		}
		// A suspend (wait-step park) is not a retryable failure — return it
		// immediately so the run parks instead of minting duplicate waitpoints.
		var susp *suspendError
		if errors.As(err, &susp) {
			return out, costSum, dur, err
		}
		lastOut, lastDur, lastErr = out, dur, err
		// Predictive cost guard: stop BEFORE the attempt that would breach
		// the cap, not after. If the running total is already at the cap,
		// or the NEXT attempt (estimated at the average per-attempt cost so
		// far) would push it over, give up now. The real failure is WRAPPED,
		// not masked, so the caller still sees why the step was failing.
		if maxCost > 0 {
			spent := priorCostUSD + costSum
			avgPerAttempt := costSum / float64(attempt)
			if spent >= maxCost || spent+avgPerAttempt > maxCost {
				return lastOut, costSum, lastDur, fmt.Errorf("%s: %w",
					retryBudgetExhaustedMessage(spent, avgPerAttempt, maxCost, step.ID), lastErr)
			}
		}
		// A retry_on that failed to compile at run time (stored-row anomaly)
		// disables retries entirely — fail safe rather than retry-ANY.
		if retryOnBroken != nil || !evalRetryOn(classifier, err) || attempt == maxAttempts {
			break
		}
		// Full jitter (the default): actual sleep is uniform in [0, delay)
		// so concurrent runs hitting the same upstream don't stampede the
		// recovery moment. e.applyJitter is injectable for deterministic
		// tests; jitter:false in the policy keeps the exact schedule.
		actualDelay := delay
		if jitter {
			actualDelay = e.applyJitter(delay)
		}
		emit.emitStepRetry(ctx, step, attempt, maxAttempts, err.Error(), actualDelay)
		if !e.retrySleep(ctx, actualDelay) {
			return "", costSum, 0, ctx.Err()
		}
		delay = time.Duration(float64(delay) * factor)
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	return lastOut, costSum, lastDur, lastErr
}

// retryMaxAttemptsCeiling caps MaxAttempts to keep a runaway retry from
// monopolising the run budget.
const retryMaxAttemptsCeiling = 10

// Backoff defaults, applied to any zero-value BackoffPolicy field.
const (
	defaultRetryMaxAttempts = 3
	defaultBackoffMinMs     = 1000
	defaultBackoffMaxMs     = 60000
	defaultBackoffFactor    = 2.0
)

// defaultRetryPolicy is the policy `on_fail: retry_step` desugars to:
// three attempts, exponential backoff 1s→60s, full jitter, retry any
// error. Enough to absorb a transient blip without unbounded spend.
func defaultRetryPolicy() *RetryPolicy {
	return &RetryPolicy{MaxAttempts: defaultRetryMaxAttempts}
}

// stepHasExplicitRetry reports whether the author opted the step into the
// per-step retry policy — either a `retry:` block or `on_fail: retry_step`
// (which desugars to the default policy). When true, that policy OWNS the
// transient-retry concern, so the inner same-tier transient loop stands
// down to avoid multiplying provider calls (see runRunnerWithTransientRetry).
func stepHasExplicitRetry(step Step) bool {
	return step.Retry != nil || step.OnFail == OnFailRetryStep
}

// retryBudgetExhaustedMessage explains a cost-cap-triggered stop inside the
// retry loop. Distinct from the run-level costCapExceededMessage (post-step
// gate): here we stopped PREDICTIVELY, before an attempt that would breach.
// Keeps the "cost cap" phrase so downstream failure classification still
// recognises the budget stop.
func retryBudgetExhaustedMessage(spent, avgPerAttempt, cap float64, stepID string) string {
	return fmt.Sprintf("cost cap would be exceeded: $%.4f spent on step %q, next retry (~$%.4f) would breach cap $%.4f",
		spent, stepID, avgPerAttempt, cap)
}

// resolveBackoff turns a (possibly nil / partially-filled) BackoffPolicy
// into concrete loop parameters, applying defaults and clamping so the
// loop can't be handed a nonsensical schedule (max < min, factor < 1).
func resolveBackoff(bp *BackoffPolicy) (minDelay, maxDelay time.Duration, factor float64, jitter bool) {
	minMs, maxMs, f, j := defaultBackoffMinMs, defaultBackoffMaxMs, defaultBackoffFactor, true
	if bp != nil {
		if bp.MinMs > 0 {
			minMs = bp.MinMs
		}
		if bp.MaxMs > 0 {
			maxMs = bp.MaxMs
		}
		if bp.Factor > 0 {
			f = bp.Factor
		}
		if bp.Jitter != nil {
			j = *bp.Jitter
		}
	}
	if f < 1 {
		f = 1 // factor < 1 would shrink the delay each attempt; clamp to constant
	}
	minDelay = time.Duration(minMs) * time.Millisecond
	maxDelay = time.Duration(maxMs) * time.Millisecond
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	return minDelay, maxDelay, f, j
}

// retrySleep pauses the retry loop for d, returning false if the context
// is cancelled during the wait. Injectable via e.sleepFn so tests drive
// the backoff schedule without real wall-clock delays.
func (e *Executor) retrySleep(ctx context.Context, d time.Duration) bool {
	if e.sleepFn != nil {
		return e.sleepFn(ctx, d)
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// applyJitter maps a base delay to the actual sleep. Default is full
// jitter (uniform in [0, d)); injectable via e.jitterFn so tests can
// force a deterministic value.
func (e *Executor) applyJitter(d time.Duration) time.Duration {
	if e.jitterFn != nil {
		return e.jitterFn(d)
	}
	if d <= 0 {
		return 0
	}
	return time.Duration(mathrand.Int64N(int64(d)))
}

// retryAttemptsPerTier caps the same-tier transient retry loop in
// runRunnerWithTransientRetry. Two attempts is enough to absorb the
// occasional 429 / 5xx / empty-completion without dramatically
// extending the wall clock; bigger numbers tip the cost balance back
// toward "just escalate to the next tier and pay for the smarter
// model". A pipeline that wants more aggressive retries can still
// opt in via step.Retry, which runs ON TOP of this floor.
const retryAttemptsPerTier = 2

// transientRetryBackoff is the floor delay before the second attempt
// in the same tier. We add full jitter on top so concurrent runs
// hitting the same upstream don't stampede the recovery moment.
const transientRetryBackoff = 800 * time.Millisecond

// runRunnerWithTransientRetry calls the agent runner and silently
// reissues on transient-class failures (rate limit, timeout, 5xx,
// truncated empty completion). The caller sees a single result —
// either the first success or the last error — but the journal
// gets one emitStepRetry entry per retry so the rail / inspector
// shows the recovery in the run timeline.
//
// Why we treat empty output as a transient: under load, Anthropic's
// API has been observed to return success + empty body when the
// upstream cancelled the response stream. Validating "" as a real
// answer immediately escalates to a more expensive tier that will
// usually produce the same empty under the same pressure. Retrying
// once on the same tier is cheaper and typically succeeds.
func (e *Executor) runRunnerWithTransientRetry(
	ctx context.Context,
	req AgentStepRequest,
	step Step,
	emit *pipelineEmitContext,
) (AgentStepResult, error) {
	var (
		res     AgentStepResult
		err     error
		lastErr error
	)
	// Composition guard: when the step carries an explicit retry policy, that
	// policy owns transient retries (with the author's backoff + retry_on), so
	// this inner same-tier loop stands down to a single attempt. Otherwise a
	// step with retry.max_attempts=N and T tiers could reach N × T × 2 provider
	// calls; standing down bounds it to N × T (the escalation chain is a
	// separate concern and still runs).
	perTier := retryAttemptsPerTier
	if stepHasExplicitRetry(step) {
		perTier = 1
	}
	for attempt := 1; attempt <= perTier; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return AgentStepResult{}, cerr
		}
		res, err = e.runner.RunStep(ctx, req)
		if err != nil {
			lastErr = err
			if attempt < perTier && isTransientRunnerError(err) {
				emit.emitStepRetry(ctx, step, attempt, perTier, err.Error(), transientRetryBackoff)
				if !sleepWithJitter(ctx, transientRetryBackoff) {
					return AgentStepResult{}, ctx.Err()
				}
				continue
			}
			return res, err
		}
		// Empty success body — treat as transient on first attempt.
		if attempt < perTier && strings.TrimSpace(res.Output) == "" {
			lastErr = fmt.Errorf("agent runner returned empty output")
			emit.emitStepRetry(ctx, step, attempt, perTier, "empty output", transientRetryBackoff)
			if !sleepWithJitter(ctx, transientRetryBackoff) {
				return AgentStepResult{}, ctx.Err()
			}
			continue
		}
		return res, nil
	}
	if err != nil {
		return res, err
	}
	return res, lastErr
}

// transientErrorMarkers names the substrings that classify a runner
// error as worth retrying on the same tier. Lowercased and matched
// case-insensitively. Curated from real failures on the dev VM (rate
// limits and load-shedding 5xx from upstream LLM providers, plus
// container-network blips from Docker).
var transientErrorMarkers = []string{
	"429",
	"rate limit",
	"rate_limit",
	"too many requests",
	"timeout",
	"timed out",
	"deadline exceeded",
	"500", "502", "503", "504",
	"internal server error",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"connection refused",
	"connection reset",
	"broken pipe",
	"eof",
}

// isTransientRunnerError reports whether `err` matches one of the
// markers we treat as worth retrying. context.Cancelled / DeadlineExceeded
// are NOT transient for this purpose — those mean the caller wanted
// the work to stop, and retrying would race against the cleanup.
func isTransientRunnerError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "context deadline") {
		return false
	}
	for _, marker := range transientErrorMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// sleepWithJitter sleeps for a random duration in [delay/2, delay)
// or returns false if the context is cancelled during the wait.
// Half-base + random keeps collisions between concurrent retries
// rare without making any one retry pathologically slow.
func sleepWithJitter(ctx context.Context, delay time.Duration) bool {
	half := delay / 2
	jittered := half + time.Duration(mathrand.Int64N(int64(delay-half+1)))
	t := time.NewTimer(jittered)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// injectValidationFeedback prepends a short "previous attempt failed"
// block to the original prompt. The block is fenced with [...] so it
// reads as out-of-band guidance, not as part of the user task. We
// keep the original prompt verbatim below so the new tier sees the
// same task description plus the new constraint.
//
// Length is capped: a long-winded grader feedback would otherwise
// dilute the prompt and waste tokens. 600 bytes is enough for the
// rubric reason + a sentence of context. Truncation walks back to
// the nearest UTF-8 rune boundary so a grader reply ending mid-emoji
// (or any multi-byte char) doesn't ship a malformed string into the
// next worker call. Mirrors the pattern in truncateForGraderLog.
func injectValidationFeedback(prompt, reason string) string {
	const maxReason = 600
	r := strings.TrimSpace(reason)
	if r == "" {
		return prompt
	}
	if len(r) > maxReason {
		cut := maxReason - 1
		for cut > 0 && cut > maxReason-5 && (r[cut]&0xc0) == 0x80 {
			cut--
		}
		r = r[:cut] + "…"
	}
	return "[PREVIOUS ATTEMPT FAILED VALIDATION: " + r + "\n" +
		"Address the failure exactly in this response. Do not repeat the same mistake.]\n\n" +
		prompt
}
