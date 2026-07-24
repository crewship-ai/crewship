package pipeline

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
)

// retry_on is a CEL predicate over a failed step's error. Unlike the
// CelCodeRunner env (which exposes `inputs`), this env exposes the
// error the runner returned so an author can scope retries precisely:
//
//	error     (string) — err.Error()
//	transient (bool)   — isTransientRunnerError(err): the built-in
//	                     429/5xx/timeout/net-blip classifier
//	status    (int)    — HTTP status parsed from the error (0 if none);
//	                     lets an http step retry on `status == 429` or
//	                     `status >= 500` without substring-matching
//
// The expression must evaluate to bool. Empty retry_on = retry any
// error (compileRetryOn returns a nil program, evalRetryOn treats nil
// as "always retry"). Compilation is validated at save time
// (validateRetryPolicy) so a bad predicate is rejected loudly rather
// than silently disabling retries at 3am.
var (
	retryCelEnv     *cel.Env
	retryCelEnvErr  error
	retryCelEnvOnce sync.Once
)

func getRetryCelEnv() (*cel.Env, error) {
	retryCelEnvOnce.Do(func() {
		retryCelEnv, retryCelEnvErr = cel.NewEnv(
			cel.Variable("error", cel.StringType),
			cel.Variable("transient", cel.BoolType),
			cel.Variable("status", cel.IntType),
		)
	})
	return retryCelEnv, retryCelEnvErr
}

// httpStatusRE matches the http runner's explicit "HTTP <code>" failure
// format (runner_http.go: `got HTTP %d`). We deliberately match ONLY this
// shape — a bare "\b\d{3}\b" fallback would misread request IDs, byte
// counts, model names, or timings ("...took 503 units") as a status and
// silently change retry behaviour. `status` is 0 for any error that isn't
// an http step's status failure; `transient` / `error.contains(...)` cover
// the rest.
var httpStatusRE = regexp.MustCompile(`(?i)\bHTTP (\d{3})\b`)

// extractHTTPStatus pulls an HTTP status code out of an error message,
// returning 0 when the error doesn't carry an explicit "HTTP <code>".
func extractHTTPStatus(msg string) int {
	if m := httpStatusRE.FindStringSubmatch(msg); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// retryOnCache holds compiled retry_on cel.Program values keyed by the raw
// expression string. Unlike schemaCache (json schema, potentially large —
// keyed by sha256 to bound the map key size), retry_on expressions are short
// authored predicates, so the expr string itself is a fine key directly.
//
// Only successful compiles are cached: a compile failure here means the
// stored row's retry_on is malformed despite save-time validation (a rare
// anomaly per compileRetryOn's doc), not a hot path worth memoizing.
// sync.Map fits the access pattern: write-once per distinct expression,
// read-many afterward across every retry attempt of every run that uses it.
// See issue #1411 — previously recompiled every runStepWithRetry call.
var retryOnCache sync.Map // map[string]cel.Program

// compileRetryOn compiles a retry_on predicate. Empty/whitespace expr
// returns (nil, nil): the caller treats a nil program as "retry any
// error", preserving the historical default. A non-bool result type is
// a compile error so `retry_on: "error"` (a string, not a predicate)
// is caught at authoring time.
func compileRetryOn(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	if v, ok := retryOnCache.Load(expr); ok {
		return v.(cel.Program), nil
	}
	env, err := getRetryCelEnv()
	if err != nil {
		return nil, fmt.Errorf("retry_on: cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("retry_on: %w", iss.Err())
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("retry_on must evaluate to bool, got %s", ast.OutputType())
	}
	prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
	if err != nil {
		return nil, fmt.Errorf("retry_on: program: %w", err)
	}
	retryOnCache.Store(expr, prg)
	return prg, nil
}

// evalRetryOn decides whether err qualifies for retry under prg. A nil
// program (empty retry_on) always retries. An eval error, or a
// non-bool result, is treated as "do not retry" — fail safe, since we
// can't confirm the author intended to retry this class of error.
func evalRetryOn(prg cel.Program, err error) bool {
	if err == nil {
		return false
	}
	if prg == nil {
		// No retry_on: retry any transient EXECUTION error by default, but a
		// tiers-exhausted validation/outcomes failure is terminal (#1429,
		// 2.10) — retrying re-runs the whole worker+grader escalation chain.
		if errors.Is(err, errStepOutcomeExhausted) {
			return false
		}
		return true
	}
	msg := err.Error()
	out, _, evalErr := prg.Eval(map[string]any{
		"error":     msg,
		"transient": isTransientRunnerError(err),
		"status":    extractHTTPStatus(msg),
	})
	if evalErr != nil {
		return false
	}
	b, ok := out.Value().(bool)
	return ok && b
}
