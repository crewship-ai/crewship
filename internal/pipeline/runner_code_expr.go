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
// string literals, typed selectors, or a CREWSHIP_INPUT_* env key resolved
// from InputEnv. Input values are never parsed as source. This is the wake-gate/probe surface (e.g. cost-spike-probe:
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
				"(see docs/manifest/routine.md `Code steps`)", req.Runtime)
	}

	code, err := normalizeCodeSource(req.Code)
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	out, err := evalTypedExprBool(code, req.InputEnv, codeVariables(req))
	if err != nil {
		return CodeRunResult{Stderr: err.Error(), ExitCode: 1}, err
	}
	s := "false"
	if out {
		s = "true"
	}
	return CodeRunResult{Stdout: s, ExitCode: 0}, nil
}

func evalTypedExprBool(code string, env map[string]string, variables map[string]any) (bool, error) {
	s := strings.TrimSpace(code)
	if s == "" {
		return false, fmt.Errorf("expr: empty expression")
	}
	i, op, err := exprComparison(s)
	if err != nil {
		return false, err
	}
	rawLeft := strings.TrimSpace(s[:i])
	rawRight := strings.TrimSpace(s[i+len(op):])
	if rawLeft == "" || rawRight == "" {
		return false, fmt.Errorf("expr: malformed comparison %q", s)
	}
	left, err := resolveCodeOperand(rawLeft, env, variables)
	if err != nil {
		return false, err
	}
	right, err := resolveCodeOperand(rawRight, env, variables)
	if err != nil {
		return false, err
	}
	return compareOperands(left, op, right)
}

// Find the sole author-written comparison, ignoring operators inside quoted
// literals and selectors. Resolving operands happens only after this scan.
func exprComparison(source string) (int, string, error) {
	position, brackets := -1, 0
	operator := ""
	var quote byte
	for i := 0; i < len(source); i++ {
		c := source[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '[' {
			brackets++
			continue
		}
		if c == ']' {
			brackets--
			if brackets < 0 {
				return 0, "", fmt.Errorf("expr: malformed comparison")
			}
			continue
		}
		if brackets != 0 {
			continue
		}
		for _, candidate := range exprOps {
			if !strings.HasPrefix(source[i:], candidate) {
				continue
			}
			if position >= 0 {
				return 0, "", fmt.Errorf("expr: exactly one comparison is supported; use cel for compound expressions")
			}
			position, operator = i, candidate
			i += len(candidate) - 1
			break
		}
	}
	if quote != 0 || brackets != 0 {
		return 0, "", fmt.Errorf("expr: malformed comparison")
	}
	if position < 0 {
		return 0, "", fmt.Errorf("expr: no comparison operator (%s) in %q", strings.Join(exprOps, " "), source)
	}
	return position, operator, nil
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
