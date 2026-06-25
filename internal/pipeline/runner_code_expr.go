package pipeline

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ExprCodeRunner is the production CodeRunner for AGENTLESS, token-zero
// routines. It is a pure-Go, in-process, deterministic evaluator — it spins
// no container, calls no LLM, and touches no filesystem/network — so it
// trivially honours the token-zero guarantee and adds no RCE surface.
//
// It supports exactly ONE runtime, "expr": a single boolean comparison
//
//	<operand> <op> <operand>      op ∈ {>, >=, <, <=, ==, !=}
//
// emitting "true" or "false" on stdout (exit 0). Operands are numeric or
// string literals (the step body is rendered first, so {{ inputs.x }} has
// already been substituted), or a CREWSHIP_INPUT_* env key which is resolved
// from InputEnv. This is the wake-gate/probe surface (e.g. cost-spike-probe:
// "spend > threshold").
//
// Every OTHER runtime (bash | python | go) is rejected fail-closed: no
// general-purpose code execution is wired in this build. Authors who need real
// shell convert the step to type: agent_run against an agent with shell-tool
// access (see docs/manifest/routine.md). A future container-backed runner can
// be added behind this same CodeRunner interface and selected by runtime.
type ExprCodeRunner struct{}

// compile-time assertion that ExprCodeRunner satisfies the interface.
var _ CodeRunner = ExprCodeRunner{}

// comparison operators, longest-first so ">=" is matched before ">".
var exprOps = []string{">=", "<=", "==", "!=", ">", "<"}

func (ExprCodeRunner) RunCode(ctx context.Context, req CodeRunRequest) (CodeRunResult, error) {
	if err := ctx.Err(); err != nil {
		return CodeRunResult{}, err
	}
	if req.Runtime != "expr" {
		return CodeRunResult{}, fmt.Errorf(
			"code runtime %q not available in this build (no sandbox wired) — "+
				"use runtime: expr for agentless probes, or convert this step to "+
				"type: agent_run with an agent that has shell-tool access "+
				"(see docs/manifest/routine.md `Code-step limitation`)", req.Runtime)
	}

	out, err := evalExprBool(req.Code, req.InputEnv)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	s := "false"
	if out {
		s = "true"
	}
	return CodeRunResult{Stdout: s, ExitCode: 0}, nil
}

// evalExprBool parses and evaluates a single comparison. The body has already
// been rendered, so operands are concrete literals (or CREWSHIP_INPUT_* keys
// resolved from env). Unknown shapes fail closed.
func evalExprBool(code string, env map[string]string) (bool, error) {
	s := strings.TrimSpace(code)
	if s == "" {
		return false, fmt.Errorf("expr: empty expression")
	}
	for _, op := range exprOps {
		i := strings.Index(s, op)
		if i < 0 {
			continue
		}
		left := resolveOperand(strings.TrimSpace(s[:i]), env)
		right := resolveOperand(strings.TrimSpace(s[i+len(op):]), env)
		if left == "" || right == "" {
			return false, fmt.Errorf("expr: malformed comparison %q", s)
		}
		return compareOperands(left, op, right)
	}
	return false, fmt.Errorf("expr: no comparison operator (%s) in %q",
		strings.Join(exprOps, " "), s)
}

// resolveOperand returns the env value when tok is a known CREWSHIP_INPUT_* key,
// otherwise the literal token (quotes stripped).
func resolveOperand(tok string, env map[string]string) string {
	if v, ok := env[tok]; ok {
		return v
	}
	return strings.Trim(tok, `"'`)
}

func compareOperands(left, op, right string) (bool, error) {
	// Numeric comparison when both sides parse as numbers.
	lf, lerr := strconv.ParseFloat(left, 64)
	rf, rerr := strconv.ParseFloat(right, 64)
	if lerr == nil && rerr == nil {
		switch op {
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		}
	}
	// String comparison: only equality operators are meaningful.
	switch op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	default:
		return false, fmt.Errorf("expr: operator %q needs numeric operands, got %q and %q", op, left, right)
	}
}
