package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"sync/atomic"
	"time"
)

// evalIfCondition decides whether a step.If render result counts as
// "true". Empty + the obvious falsey strings short-circuit to false;
// everything else is true. Case-insensitive to match how YAML/JSON
// values flow through templates ("False" from a Python service still
// reads as falsey).
//
// Mirrors GitHub Actions' `if:` evaluator on the easy cases (no full
// expression language — that's a deeper rabbit hole and Render
// already covers the substitution side).
func evalIfCondition(rendered string) bool {
	s := rendered
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	if s == "" {
		return false
	}
	// ASCII fold for the falsey-literal check
	low := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	switch string(low) {
	case "false", "0", "null", "nil", "no", "off":
		return false
	}
	return true
}

// renderConcurrencyKey renders the DSL's concurrency_key template
// against the inputs map. We only support `{{ inputs.X }}` here —
// the full Render pipeline isn't reachable yet (no step outputs at
// reservation time).
//
// Three outcomes:
//
//   - template == ""                 → ("", false, nil)  unset by author, no gate
//   - non-empty template, renders OK → (key, true, nil)  gate engaged
//   - non-empty template, empty out  → ("", true, err)   author asked for a gate
//     but a referenced input is missing/empty — that's a silent-bypass bug if
//     we let it through, so caller MUST fail the run.
//
// Why: a routine that declares `concurrency_key: "{{ inputs.account_id }}"`
// is asking the platform to serialise runs per tenant. If account_id is
// missing, returning empty key (no gate) would silently allow unlimited
// parallelism — a Denial-of-Self via unintended fan-out. The fail-fast
// here matches Trigger.dev's behaviour: a queue/concurrencyKey with no
// resolved value is a config error, not "no gate."
func renderConcurrencyKey(_ context.Context, template string, inputs map[string]any) (string, bool, error) {
	if template == "" {
		return "", false, nil
	}
	rc := RenderContext{Inputs: inputs, StepOutputs: map[string]string{}, Env: map[string]string{}}
	rendered := Render(template, rc)
	if rendered == "" {
		return "", true, ErrConcurrencyKeyEmpty
	}
	return rendered, true, nil
}

// estimateStepCost returns a coarse cost guess for a dry-run step.
// MVP uses a flat per-step number; Phase 2 will read pricing from
// internal/llm and produce model-aware estimates with token counts.
func estimateStepCost(_ Step, prompt string) float64 {
	// Rough heuristic: $1/M input tokens, ~4 chars/token. Output
	// guess at 25% of input. This is order-of-magnitude only — the
	// dry-run report explicitly labels it "estimated" so users
	// don't mistake it for a quote.
	tokensIn := float64(len(prompt)) / 4
	tokensOut := tokensIn * 0.25
	return (tokensIn + tokensOut) / 1_000_000
}

// mergeInputs takes the caller-supplied inputs and merges in the DSL's
// declared defaults so templates can reference any input the DSL
// promised, even when the caller omitted optional fields.
func mergeInputs(supplied map[string]any, dsl *DSL) map[string]any {
	out := make(map[string]any, len(dsl.Inputs))
	for _, spec := range dsl.Inputs {
		if v, ok := supplied[spec.Name]; ok {
			out[spec.Name] = v
			continue
		}
		if spec.Default != nil {
			out[spec.Name] = spec.Default
		}
	}
	// Preserve any extra inputs the caller passed that the DSL
	// didn't declare — useful for ad-hoc test runs.
	for k, v := range supplied {
		if _, already := out[k]; !already {
			out[k] = v
		}
	}
	return out
}

// generateRunID mints a "run_" CUID for journaling. Distinct from
// generatePipelineID so journal queries can pattern-match either
// kind without ambiguity.
func generateRunID() string {
	ts := time.Now().UnixMilli()
	c := runIDCounter.Add(1)
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b[0] = byte(c)
	}
	var buf [40]byte
	out := append(buf[:0], 'r', 'u', 'n', '_', 'c')
	out = strconv.AppendInt(out, ts, 36)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	out = append(out,
		hexdigits[(tail>>12)&0xf],
		hexdigits[(tail>>8)&0xf],
		hexdigits[(tail>>4)&0xf],
		hexdigits[tail&0xf],
	)
	out = append(out, hex.EncodeToString(b)...)
	return string(out)
}

var runIDCounter atomic.Uint64
