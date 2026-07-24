package pipeline

import (
	"fmt"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
)

// evalStepCondition decides whether a step's `if:` gate passes (#1419, part 2).
//
// Three evaluation modes, chosen so existing routines keep byte-identical
// behaviour while new ones get real expressions:
//
//   - TEMPLATE form (`if: "{{ inputs.dry_run }}"`): the string carries a
//     {{ ... }} placeholder, which is NOT valid CEL. It stays on the historical
//     render-then-truthy path — the substitution is resolved and the result is
//     falsey-checked (empty/false/0/null/nil/no/off → skip).
//   - CEL form (`if: 'inputs.x == "y"'`): a bare expression with no braces is
//     compiled + evaluated against the typed variables `inputs`, `steps`, `run`.
//     A bool result is used directly; a non-bool (number/string) result is run
//     through the same truthy rule so `if: inputs.count` behaves sensibly.
//   - BARE STRING fallback (`if: "yes"`): a string that is neither a template
//     nor a compilable/evaluable CEL expression falls back to the truthy check
//     on the literal — so a plain "true"/"1"/"yes" still gates as before.
//
// This closes the long-standing gap where `if: inputs.x == "y"` (no braces, no
// CEL) rendered to itself and the non-empty literal always read as truthy —
// i.e. the condition silently never gated. Now it is evaluated.
func evalStepCondition(rawIf string, ctx RenderContext) bool {
	if strings.TrimSpace(rawIf) == "" {
		return true // no condition ⇒ run
	}
	// Template form: braces aren't valid CEL, and these are the pre-CEL
	// routines — resolve substitutions then apply the truthy rule.
	if strings.Contains(rawIf, "{{") {
		return evalIfCondition(Render(rawIf, ctx))
	}
	if res, ok := evalIfCEL(rawIf, ctx); ok {
		return res
	}
	// Not compilable/evaluable CEL (a bare word like "yes", or a lone "0"):
	// fall back to the historical truthy check on the raw literal.
	return evalIfCondition(rawIf)
}

// evalIfCEL compiles + evaluates a CEL `if:` expression. Returns (result, true)
// on a usable evaluation, or (_, false) when the expression is not valid CEL /
// errored at eval — signalling the caller to fall back to the truthy check.
func evalIfCEL(expr string, ctx RenderContext) (bool, bool) {
	// Compile-bomb guard, same rationale as the code runner: the raw `if:`
	// is author-authored (not webhook-inflated) but bounding the source is
	// cheap insurance against a pathological expression.
	if len(expr) > maxCELSourceBytes {
		return false, false
	}
	prg, err := compileIfCEL(expr)
	if err != nil {
		return false, false
	}
	out, _, err := prg.Eval(ifCELVars(ctx))
	if err != nil {
		// A compiled-but-errored eval (e.g. a missing map key) is ambiguous;
		// signal fallback rather than guessing.
		return false, false
	}
	switch v := out.Value().(type) {
	case bool:
		return v, true
	default:
		s, serr := celStringify(out)
		if serr != nil {
			return false, false
		}
		return evalIfCondition(s), true
	}
}

// ifCELVars builds the variable bindings a step `if:` expression sees:
//
//	inputs — the run's merged inputs (json.Number coerced for arithmetic)
//	steps  — map of step_id → that step's output STRING (steps.classify == "spam")
//	run    — {metadata: {...}, is_replay: bool, replay_of, run_id} from run context
func ifCELVars(ctx RenderContext) map[string]any {
	steps := make(map[string]any, len(ctx.StepOutputs))
	for k, v := range ctx.StepOutputs {
		steps[k] = v
	}
	run := map[string]any{}
	if ctx.Metadata != nil {
		run["metadata"] = celValue(ctx.Metadata)
	} else {
		run["metadata"] = map[string]any{}
	}
	// Mirror the {{ run.* }} scalars the template namespace exposes.
	run["is_replay"] = ctx.Env["is_replay"] == "true"
	run["replay_of"] = ctx.Env["replay_of"]
	run["run_id"] = ctx.Env["run_id"]
	return map[string]any{
		"inputs": celInputs(ctx.Inputs),
		"steps":  steps,
		"run":    run,
	}
}

// ifCELEnv is the shared CEL environment for step `if:` expressions. Declares
// the three typed variables (inputs/steps/run) as dyn maps so field access and
// comparison work without per-step schema wiring. Built once.
var (
	ifCELEnvOnce sync.Once
	ifCELEnv     *cel.Env
	ifCELEnvErr  error
)

func getIfCELEnv() (*cel.Env, error) {
	ifCELEnvOnce.Do(func() {
		ifCELEnv, ifCELEnvErr = cel.NewEnv(
			cel.Variable("inputs", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("steps", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("run", cel.MapType(cel.StringType, cel.DynType)),
		)
	})
	return ifCELEnv, ifCELEnvErr
}

// ifCELCache memoizes compiled `if:` programs by expression text so a step
// that fires repeatedly with unchanging source pays the compile cost once.
// Bounded + flushed-wholesale on overflow, mirroring the code runner's cache.
var (
	ifCELCacheMu sync.Mutex
	ifCELCache   = make(map[string]cel.Program, celProgramCacheMax)
)

func compileIfCEL(expr string) (cel.Program, error) {
	ifCELCacheMu.Lock()
	if prg, ok := ifCELCache[expr]; ok {
		ifCELCacheMu.Unlock()
		return prg, nil
	}
	ifCELCacheMu.Unlock()

	env, err := getIfCELEnv()
	if err != nil {
		return nil, fmt.Errorf("if: cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("if: compile: %w", iss.Err())
	}
	prg, err := env.Program(ast, cel.CostLimit(celCostLimit))
	if err != nil {
		return nil, fmt.Errorf("if: program: %w", err)
	}

	ifCELCacheMu.Lock()
	if len(ifCELCache) >= celProgramCacheMax {
		ifCELCache = make(map[string]cel.Program, celProgramCacheMax)
	}
	ifCELCache[expr] = prg
	ifCELCacheMu.Unlock()
	return prg, nil
}
