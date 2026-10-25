package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
)

// CelCodeRunner is the production CodeRunner for AGENTLESS, token-zero
// routines that need more than a single comparison. It evaluates a
// Google CEL (Common Expression Language) expression against the
// routine's inputs.
//
// CEL is non-Turing-complete (every expression provably terminates),
// pure-Go, and sandboxed by construction — no filesystem, no network,
// no loops, no I/O. That preserves the token-zero guarantee and adds
// no RCE surface, while giving authors real logic: boolean operators
// (&&, ||, !), arithmetic, string ops, list/map membership, ternary,
// and field access. It is the general agentless-logic primitive that
// `expr` (one comparison) could not express.
//
// Source is author-owned. Inputs and prior outputs enter as typed variables;
// legacy simple references are translated to selectors without rendering data.
//
// Output: a bool result emits "true"/"false"; numeric and string
// results emit their canonical string form. This mirrors the expr
// runner's wake-gate contract so a probe can drive a schedule gate.
type CelCodeRunner struct{}

// celCostLimit bounds CEL evaluation cost — generous for real probe/
// gate expressions, but a hard ceiling against a pathological one.
const celCostLimit = 1_000_000

// maxCELSourceBytes bounds author source and its normalized form before
// compilation. Evaluation cost limits do not bound compilation cost.
const maxCELSourceBytes = 4096

// celProgramCacheMax bounds the compiled-program cache so a stream of
// distinct (e.g. input-templated) expressions can't grow it without limit.
// On overflow the cache is flushed wholesale — simple, allocation-cheap, and
// good enough for the steady-state case the cache targets (a fixed set of
// recurring expression texts).
const celProgramCacheMax = 256

var _ CodeRunner = CelCodeRunner{}

// celEnv is the single CEL environment shared across all evaluations — its
// fixed, scoped namespace declarations never change, so there is
// no reason to rebuild it per call. Built once via celEnvOnce.
var (
	celEnvOnce sync.Once
	celEnv     *cel.Env
	celEnvErr  error
)

// getCELEnv lazily constructs (once) and returns the shared CEL environment.
func getCELEnv() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		options := make([]cel.EnvOption, 0, len(codeNamespaces))
		for _, name := range codeNamespaces {
			options = append(options, cel.Variable(name, cel.MapType(cel.StringType, cel.DynType)))
		}
		celEnv, celEnvErr = cel.NewEnv(options...)
	})
	return celEnv, celEnvErr
}

// celProgramCache memoizes compiled cel.Programs keyed by expression text so
// a routine whose CEL step fires repeatedly with unchanging source pays the
// env-construction + compile cost once, not per eval (finding M4). cel.Program
// is safe for concurrent Eval, so cached entries are shared across goroutines;
// access to the map itself is guarded by celCacheMu.
var (
	celCacheMu sync.Mutex
	celCache   = make(map[string]cel.Program, celProgramCacheMax)
)

// compileCELProgram returns a compiled, cost-limited program for the given
// (already length-checked) expression text, building+caching it on first use.
func compileCELProgram(code string) (cel.Program, error) {
	celCacheMu.Lock()
	if prg, ok := celCache[code]; ok {
		celCacheMu.Unlock()
		return prg, nil
	}
	celCacheMu.Unlock()

	env, err := getCELEnv()
	if err != nil {
		return nil, fmt.Errorf("cel: env: %w", err)
	}
	ast, iss := env.Compile(code)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("cel: compile: %w", iss.Err())
	}
	// Cost limit: CEL is already non-Turing-complete (guaranteed to
	// terminate), but cap the evaluation cost as defense-in-depth so a
	// pathological expression (deeply nested list/map ops) can't burn
	// unbounded CPU on the request goroutine.
	prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
	if err != nil {
		return nil, fmt.Errorf("cel: program: %w", err)
	}

	celCacheMu.Lock()
	// Flush wholesale on overflow rather than tracking per-entry recency —
	// keeps the cache bounded with negligible bookkeeping.
	if len(celCache) >= celProgramCacheMax {
		celCache = make(map[string]cel.Program, celProgramCacheMax)
	}
	celCache[code] = prg
	celCacheMu.Unlock()
	return prg, nil
}

// RunCode compiles + evaluates the CEL expression in req.Code against
// the typed `inputs` variable and returns the canonical string form of
// the result (bool → "true"/"false", numbers/strings verbatim).
func (CelCodeRunner) RunCode(ctx context.Context, req CodeRunRequest) (CodeRunResult, error) {
	if err := ctx.Err(); err != nil {
		return CodeRunResult{}, err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		err := fmt.Errorf("cel: empty expression")
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	// Reject oversized source before parsing or compatibility translation.
	if len(code) > maxCELSourceBytes {
		err := fmt.Errorf("cel: expression too large: %d bytes (max %d)", len(code), maxCELSourceBytes)
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	code, err := normalizeCodeSource(code)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	if len(code) > maxCELSourceBytes {
		err := fmt.Errorf("cel: normalized expression too large: %d bytes (max %d)", len(code), maxCELSourceBytes)
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}

	// Compile once, cache by expression text (M4) — repeated firings of a
	// fixed-source step reuse the compiled program instead of rebuilding
	// env + AST + program every call.
	prg, err := compileCELProgram(code)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}

	out, _, err := prg.Eval(codeVariables(req))
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, fmt.Errorf("cel: eval: %w", err)
	}

	s, err := celStringify(out)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	return CodeRunResult{Stdout: s, ExitCode: 0}, nil
}

// celInputs normalizes the render-context inputs into CEL-friendly Go
// values. The main job is coercing json.Number (which the DSL decodes
// with UseNumber to preserve precision) into int64/float64 so CEL's
// arithmetic and comparison operators accept it.
func celInputs(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = celValue(v)
	}
	return out
}

// celValue normalizes one decoded JSON value (recursively) into a
// CEL-friendly Go value — chiefly coercing json.Number to int64/float64.
func celValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		return celInputs(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = celValue(t[i])
		}
		return out
	default:
		return v
	}
}

// celStringify renders a CEL result to the canonical string form the
// pipeline's downstream steps expect (bool → "true"/"false", numbers
// without trailing noise, strings verbatim).
func celStringify(out ref.Val) (string, error) {
	v := out.Value()
	switch t := v.(type) {
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	case string:
		return t, nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	default:
		return fmt.Sprintf("%v", t), nil
	}
}
