package pipeline

import (
	"fmt"
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
		)
	})
	return retryCelEnv, retryCelEnvErr
}

// compileRetryOn compiles a retry_on predicate. Empty/whitespace expr
// returns (nil, nil): the caller treats a nil program as "retry any
// error", preserving the historical default. A non-bool result type is
// a compile error so `retry_on: "error"` (a string, not a predicate)
// is caught at authoring time.
func compileRetryOn(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
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
		return true
	}
	out, _, evalErr := prg.Eval(map[string]any{
		"error":     err.Error(),
		"transient": isTransientRunnerError(err),
	})
	if evalErr != nil {
		return false
	}
	b, ok := out.Value().(bool)
	return ok && b
}
