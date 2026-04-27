package reflection

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// DefaultMaxIterations is the conventional cap for the evaluator-optimizer
// loop. Five iterations is the sweet spot from published evaluator-
// optimizer results: enough runway to escape a bad first draft, short
// enough that a truly stuck generator escalates rather than burning
// tokens indefinitely.
const DefaultMaxIterations = 5

// Generator produces a candidate output given a prompt and a list of
// supporting context strings. Context typically carries prior-attempt
// feedback (see EvaluatorLoop) but can also carry fixed reference
// material the generator needs every iteration.
type Generator interface {
	Generate(ctx context.Context, prompt string, context []string) (string, error)
}

// Verifier evaluates a candidate and reports pass/fail plus guidance for
// the next iteration. Status values are the strings "pass" and "fail";
// a caller that receives anything else treats it as fail to stay on the
// safe side.
type Verifier interface {
	Verify(ctx context.Context, subject string) (VerifyResult, error)
}

// VerifyResult is the Verifier's output. Status "pass" terminates the
// loop. On "fail" Issues describes what went wrong and SuggestedFix is an
// optional hint the loop threads back into the Generator's context.
type VerifyResult struct {
	Status       string
	Issues       []string
	SuggestedFix string
}

// ErrEvaluatorLoopExhausted is returned when the loop runs out of
// iterations without seeing a pass. Callers typically treat this as a
// signal to escalate (Harbor Master approval, human review, etc.) rather
// than silently accepting the last candidate.
var ErrEvaluatorLoopExhausted = errors.New("reflection: evaluator loop exhausted without pass")

// EvaluatorLoop runs Generator → Verifier until Verify returns pass or
// iterations is exhausted.
//
// The loop:
//  1. Treats initial as iteration 0's candidate (if non-empty) rather
//     than running Generate first. This lets a caller who already has
//     a draft skip straight to verification.
//  2. On fail, prepends a feedback block of the form
//     "Previous attempt failed with: <issues>. Suggested: <fix>"
//     to the generator's context slice, and calls Generate again.
//  3. Emits an EntryEvalMetric to the journal for every iteration,
//     carrying iteration number, status, issues count, and the number
//     of context entries fed to the generator.
//
// Returns the last candidate (whether passing or not), the iteration
// count actually consumed (1-based; 0 means "never ran"), and an error.
// On exhaustion the error is ErrEvaluatorLoopExhausted and the caller
// still receives the last candidate so it can escalate with a real
// artefact rather than an empty string.
//
// If maxIters <= 0 the caller's intent is ambiguous; we default to
// DefaultMaxIterations rather than looping zero or infinite times.
func EvaluatorLoop(
	ctx context.Context,
	gen Generator,
	ver Verifier,
	j journal.Emitter,
	initial string,
	prompt string,
	maxIters int,
	scope Scope,
) (string, int, error) {
	if gen == nil {
		return "", 0, fmt.Errorf("reflection: EvaluatorLoop requires a generator")
	}
	if ver == nil {
		return "", 0, fmt.Errorf("reflection: EvaluatorLoop requires a verifier")
	}
	if maxIters <= 0 {
		maxIters = DefaultMaxIterations
	}
	// j is allowed to be nil so tight-loop callers can opt out of
	// journaling; guard every emit below.

	candidate := initial
	var feedback []string
	var lastErr error

	for i := 0; i < maxIters; i++ {
		// Generate on iter 0 only if no initial was provided; otherwise
		// carry the initial candidate straight into verification.
		if !(i == 0 && candidate != "") {
			out, err := gen.Generate(ctx, prompt, append([]string(nil), feedback...))
			if err != nil {
				emitLoopMetric(ctx, j, scope, i+1, "generator_error", nil, len(feedback))
				return candidate, i, fmt.Errorf("reflection: generate iter %d: %w", i+1, err)
			}
			candidate = out
		}

		result, err := ver.Verify(ctx, candidate)
		if err != nil {
			emitLoopMetric(ctx, j, scope, i+1, "verifier_error", nil, len(feedback))
			return candidate, i + 1, fmt.Errorf("reflection: verify iter %d: %w", i+1, err)
		}

		emitLoopMetric(ctx, j, scope, i+1, result.Status, result.Issues, len(feedback))

		if strings.EqualFold(result.Status, "pass") {
			return candidate, i + 1, nil
		}

		// Fail path: fold feedback into the context for the next Generate.
		feedback = append(feedback, formatFeedback(result))
		lastErr = fmt.Errorf("iter %d: %s", i+1, strings.Join(result.Issues, "; "))
	}

	return candidate, maxIters, fmt.Errorf("%w: %v", ErrEvaluatorLoopExhausted, lastErr)
}

// formatFeedback turns a failing VerifyResult into the exact string that
// gets prepended to the next Generate's context. Keeping the format in
// one place makes it easy for tests to assert on — and easy for prompt
// engineers to tune the wording without hunting through call sites.
func formatFeedback(r VerifyResult) string {
	issues := "unspecified"
	if len(r.Issues) > 0 {
		issues = strings.Join(r.Issues, "; ")
	}
	if r.SuggestedFix != "" {
		return fmt.Sprintf("Previous attempt failed with: %s. Suggested: %s", issues, r.SuggestedFix)
	}
	return fmt.Sprintf("Previous attempt failed with: %s.", issues)
}

// emitLoopMetric writes a single EntryEvalMetric entry capturing one
// iteration's result. The emit is best-effort — we never let journal
// failure abort the loop.
func emitLoopMetric(ctx context.Context, j journal.Emitter, scope Scope, iter int, status string, issues []string, ctxSize int) {
	if j == nil || scope.WorkspaceID == "" {
		return
	}
	payload := map[string]any{
		"metric":          "evaluator_loop",
		"iteration":       iter,
		"status":          status,
		"issues_count":    len(issues),
		"context_entries": ctxSize,
	}
	entry := journal.Entry{
		WorkspaceID: scope.WorkspaceID,
		CrewID:      scope.CrewID,
		AgentID:     scope.AgentID,
		MissionID:   scope.MissionID,
		Type:        journal.EntryEvalMetric,
		ActorType:   journal.ActorOrchestrator,
		ActorID:     "evaluator_loop",
		Severity:    journal.SeverityInfo,
		Summary:     fmt.Sprintf("evaluator_loop iter %d: %s", iter, status),
		Payload:     payload,
		Refs: map[string]any{
			"reflection": true,
			"stage":      "evaluator_loop",
		},
	}
	_, _ = j.Emit(ctx, entry)
}
