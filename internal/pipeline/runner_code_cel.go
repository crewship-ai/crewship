package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"
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
// The step body is rendered (so {{ inputs.x }} substitutes) BEFORE it
// reaches the runner, but CEL authors typically reference the typed
// `inputs` variable directly (inputs.spend_usd > inputs.threshold_usd)
// so numbers stay numbers for arithmetic.
//
// Output: a bool result emits "true"/"false"; numeric and string
// results emit their canonical string form. This mirrors the expr
// runner's wake-gate contract so a probe can drive a schedule gate.
type CelCodeRunner struct{}

var _ CodeRunner = CelCodeRunner{}

func (CelCodeRunner) RunCode(ctx context.Context, req CodeRunRequest) (CodeRunResult, error) {
	if err := ctx.Err(); err != nil {
		return CodeRunResult{}, err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		err := fmt.Errorf("cel: empty expression")
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}

	env, err := cel.NewEnv(
		cel.Variable("inputs", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, fmt.Errorf("cel: env: %w", err)
	}

	ast, iss := env.Compile(code)
	if iss != nil && iss.Err() != nil {
		err := fmt.Errorf("cel: compile: %w", iss.Err())
		return CodeRunResult{Stderr: iss.Err().Error(), ExitCode: 1}, err
	}
	prg, err := env.Program(ast)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, fmt.Errorf("cel: program: %w", err)
	}

	out, _, err := prg.Eval(map[string]any{"inputs": celInputs(req.Inputs)})
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
