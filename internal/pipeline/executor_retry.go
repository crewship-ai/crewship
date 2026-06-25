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
) (string, float64, int64, error) {
	rp := step.Retry
	if rp == nil || rp.MaxAttempts <= 1 {
		return e.runStep(ctx, step, renderedPrompt, primary, fallback, in, runID, pipelineID, emit, parentRender, depth)
	}

	maxAttempts := rp.MaxAttempts
	if maxAttempts > 10 {
		// Cap to keep a runaway retry from monopolising the run
		// budget. 10 attempts is the conventional ceiling.
		maxAttempts = 10
	}
	initialDelay := time.Duration(rp.InitialDelayMs) * time.Millisecond
	if initialDelay <= 0 {
		initialDelay = time.Second
	}
	maxDelay := time.Duration(rp.MaxDelayMs) * time.Millisecond
	if maxDelay <= 0 {
		maxDelay = time.Minute
	}

	var (
		lastOut string
		lastDur int64
		lastErr error
		costSum float64
	)
	delay := initialDelay
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
		if !shouldRetry(err, rp.RetryOn) || attempt == maxAttempts {
			break
		}
		emit.emitStepRetry(ctx, step, attempt, err.Error(), delay)
		// Full jitter: actual sleep is uniform in [0, delay). Without
		// jitter, N agents that hit the same upstream 429/5xx all
		// retry in lockstep and stampede the recovery moment. The
		// AWS Architecture Blog post on "Exponential Backoff And
		// Jitter" documents the canonical analysis. We keep the
		// deterministic upper bound for tests by floor'ing very
		// small delays.
		actualDelay := delay
		if delay > 50*time.Millisecond {
			actualDelay = time.Duration(mathrand.Int64N(int64(delay)))
		}
		select {
		case <-ctx.Done():
			return "", costSum, 0, ctx.Err()
		case <-time.After(actualDelay):
		}
		if rp.Backoff == "exponential" {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
	return lastOut, costSum, lastDur, lastErr
}

// shouldRetry tests whether the error matches the policy's RetryOn
// allowlist. Empty list = retry on any error (most permissive).
// Substring match is intentional — error wrapping makes exact-match
// brittle, and the typical patterns ("timeout", "5xx", "rate limit")
// are durable substrings.
func shouldRetry(err error, retryOn []string) bool {
	if err == nil {
		return false
	}
	if len(retryOn) == 0 {
		return true
	}
	msg := err.Error()
	for _, sub := range retryOn {
		if sub == "" {
			continue
		}
		if containsCaseFold(msg, sub) {
			return true
		}
	}
	return false
}

// containsCaseFold is strings.Contains with ASCII case folding.
// Keeps "Timeout" / "timeout" / "TIMEOUT" all matching the same
// retry allowlist entry — error message casing is inconsistent
// across runners and we don't want callers second-guessing it.
func containsCaseFold(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	return indexCaseFold(s, substr) >= 0
}

func indexCaseFold(s, substr string) int {
	if len(substr) == 0 {
		return 0
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a := s[i+j]
			b := substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
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
	for attempt := 1; attempt <= retryAttemptsPerTier; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return AgentStepResult{}, cerr
		}
		res, err = e.runner.RunStep(ctx, req)
		if err != nil {
			lastErr = err
			if attempt < retryAttemptsPerTier && isTransientRunnerError(err) {
				emit.emitStepRetry(ctx, step, attempt, err.Error(), transientRetryBackoff)
				if !sleepWithJitter(ctx, transientRetryBackoff) {
					return AgentStepResult{}, ctx.Err()
				}
				continue
			}
			return res, err
		}
		// Empty success body — treat as transient on first attempt.
		if attempt < retryAttemptsPerTier && strings.TrimSpace(res.Output) == "" {
			lastErr = fmt.Errorf("agent runner returned empty output")
			emit.emitStepRetry(ctx, step, attempt, "empty output", transientRetryBackoff)
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
