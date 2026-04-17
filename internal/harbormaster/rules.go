package harbormaster

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Evaluator decides whether a tool call needs human approval. It owns a
// list of RuleMatcher values; the first matching rule wins. The default
// rule set is intentionally conservative: anything that smells like a
// production deploy, a destructive op, an expensive call, or a prod
// hostname is gated.
//
// Evaluator is read-mostly; rules are loaded at startup. If runtime rule
// changes are needed later, swap the field for an atomic.Pointer — the
// public API can stay the same.
type Evaluator struct {
	rules []RuleMatcher
}

// NewEvaluator builds an evaluator from caller-supplied rules. The
// returned evaluator does NOT include the default rules; use
// NewEvaluatorWithDefaults for the typical wiring.
func NewEvaluator(rules ...RuleMatcher) *Evaluator {
	out := make([]RuleMatcher, 0, len(rules))
	out = append(out, rules...)
	return &Evaluator{rules: out}
}

// NewEvaluatorWithDefaults wraps NewEvaluator with the three rules listed
// in the package design: tool-name pattern, cost threshold, and target
// environment. Custom rules from the caller are appended after the
// defaults so a more specific custom rule still wins by ordering at the
// caller's discretion (prepend if you want to override).
func NewEvaluatorWithDefaults(extra ...RuleMatcher) *Evaluator {
	defaults := DefaultRules()
	rules := make([]RuleMatcher, 0, len(defaults)+len(extra))
	rules = append(rules, defaults...)
	rules = append(rules, extra...)
	return &Evaluator{rules: rules}
}

// DefaultRules returns the baked-in rule set. Exported so callers can
// inspect / extend it before constructing an evaluator (e.g. mutating
// MapsToKind for telemetry).
func DefaultRules() []RuleMatcher {
	return []RuleMatcher{
		{
			Name:        "destructive-tool-name",
			ToolPattern: regexp.MustCompile(`(?i)deploy|production|delete_.*|drop_.*|terminate_.*`),
			MapsToKind:  KindDestructiveOp,
		},
		{
			Name:             "cost-over-10usd",
			CostThresholdUSD: 10,
			MapsToKind:       KindCostThreshold,
		},
		{
			Name:              "target-prod-hostname",
			TargetEnvPatterns: []string{"prod", "production", "live"},
			MapsToKind:        KindTargetEnvironment,
		},
	}
}

// Evaluate runs all rules in order and returns the first match. If no
// rule fires, required=false and kind="" are returned. The reason string
// is human-readable and is suitable for the approvals_queue.reason column
// when the caller chooses to enqueue.
//
// The ctx argument is reserved for future async predicates (e.g. policy
// service callout) — current rules are pure-Go and ignore it.
func (e *Evaluator) Evaluate(_ context.Context, tool string, args map[string]any) (required bool, reason string, kind Kind) {
	for _, r := range e.rules {
		if hit, why := r.match(tool, args); hit {
			k := r.MapsToKind
			if k == "" {
				k = KindCustom
			}
			name := r.Name
			if name == "" {
				name = string(k)
			}
			return true, fmt.Sprintf("%s: %s", name, why), k
		}
	}
	return false, "", ""
}

// match returns whether this rule fires for the given tool/args plus a
// short fragment explaining which condition tripped. The conditions are
// OR-composed: any single one is sufficient.
func (r RuleMatcher) match(tool string, args map[string]any) (bool, string) {
	if r.ToolPattern != nil && r.ToolPattern.MatchString(tool) {
		return true, fmt.Sprintf("tool %q matches %s", tool, r.ToolPattern.String())
	}
	if r.CostThresholdUSD > 0 {
		if v, ok := numericArg(args, "cost_estimate_usd"); ok && v >= r.CostThresholdUSD {
			return true, fmt.Sprintf("cost_estimate_usd=%.2f >= %.2f", v, r.CostThresholdUSD)
		}
	}
	if len(r.TargetEnvPatterns) > 0 {
		for _, key := range []string{"target", "host", "hostname", "environment", "env"} {
			s, ok := stringArg(args, key)
			if !ok {
				continue
			}
			lower := strings.ToLower(s)
			for _, pat := range r.TargetEnvPatterns {
				if pat == "" {
					continue
				}
				if strings.Contains(lower, strings.ToLower(pat)) {
					return true, fmt.Sprintf("%s=%q contains %q", key, s, pat)
				}
			}
		}
	}
	if len(r.Kinds) > 0 {
		// Rule restricts to specific Kinds — only contributes when caller
		// pre-classified the request via args["kind"]. Skip if absent.
		if k, ok := stringArg(args, "kind"); ok {
			for _, want := range r.Kinds {
				if Kind(k) == want {
					return true, fmt.Sprintf("kind=%s in allow-list", k)
				}
			}
		}
	}
	if r.RequireWhen != nil && r.RequireWhen(tool, args) {
		return true, "RequireWhen predicate matched"
	}
	return false, ""
}

// numericArg coerces an arg to float64 across the JSON-ish types Go
// returns from json.Unmarshal: float64, int, int64, json.Number-compatible
// strings. Anything unparseable returns ok=false so the rule simply
// doesn't fire on garbage input.
func numericArg(args map[string]any, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint64:
		return float64(t), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return "", false
}
