package pipeline

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// executor_render.go — renderConcurrencyKey.
//
// Used at pipeline reservation time to derive the concurrency_key for
// gating overlapping runs. Only `{{ inputs.X }}` is supported (step
// outputs aren't available at reservation time). Empty template means
// "no gate".
//
// The function looks trivial but it's load-bearing: a regression that
// produced a non-empty key from an empty template would gate ALL runs
// on the same lock; a regression that mis-rendered an `inputs.X`
// reference would silently gate distinct runs together and cause
// deadlocks.
// ---------------------------------------------------------------------------

func TestRenderConcurrencyKey_EmptyTemplate_ReturnsEmpty(t *testing.T) {
	// Empty template MUST yield empty key — the executor takes that as
	// "no concurrency gate" and dispatches without locking. A regression
	// that returned anything else (e.g. "<nil>" from a Render call on
	// empty input) would silently single-thread every run on the same
	// pipeline.
	got := renderConcurrencyKey(context.Background(), "", map[string]any{"x": "y"})
	if got != "" {
		t.Errorf("renderConcurrencyKey(\"\") = %q, want empty (no gate)", got)
	}
}

func TestRenderConcurrencyKey_EmptyTemplate_NilInputs(t *testing.T) {
	// Nil inputs must NOT cause the empty-template fast-path to throw —
	// reservation-time callers may pass nil before they've evaluated
	// step inputs.
	got := renderConcurrencyKey(context.Background(), "", nil)
	if got != "" {
		t.Errorf("nil inputs with empty template = %q, want empty", got)
	}
}

func TestRenderConcurrencyKey_LiteralTemplate_NoSubstitutions(t *testing.T) {
	// A template with no {{ ... }} substitutions passes through. Pin
	// so a future "treat every template as a key prefix" change has to
	// flip this test deliberately rather than silently breaking the
	// "static key" pattern (e.g. concurrency_key: "build-queue").
	got := renderConcurrencyKey(context.Background(), "build-queue", nil)
	if got != "build-queue" {
		t.Errorf("literal template = %q, want \"build-queue\"", got)
	}
}

func TestRenderConcurrencyKey_SubstitutesSingleInput(t *testing.T) {
	// The 1:1 case the source comment calls out — `{{ inputs.X }}`.
	// Pipelines use this to gate by user / project / branch.
	got := renderConcurrencyKey(context.Background(),
		"deploy-{{ inputs.env }}",
		map[string]any{"env": "production"},
	)
	if got != "deploy-production" {
		t.Errorf("got %q, want \"deploy-production\"", got)
	}
}

func TestRenderConcurrencyKey_SubstitutesMultipleInputs(t *testing.T) {
	// A template can splice multiple inputs together to form a
	// composite key — common pattern for "gate by (project, env)".
	got := renderConcurrencyKey(context.Background(),
		"{{ inputs.project }}-{{ inputs.env }}",
		map[string]any{"project": "alpha", "env": "staging"},
	)
	if got != "alpha-staging" {
		t.Errorf("got %q, want \"alpha-staging\"", got)
	}
}

func TestRenderConcurrencyKey_MissingInput_RendersEmpty(t *testing.T) {
	// Source: "Unknown references render as empty strings". Pin so a
	// regression to "panic on missing" or "leave the {{...}} marker"
	// surfaces immediately — both behaviors would break the gate.
	got := renderConcurrencyKey(context.Background(),
		"deploy-{{ inputs.missing }}",
		map[string]any{"present": "x"},
	)
	if got != "deploy-" {
		t.Errorf("missing-input render = %q, want \"deploy-\" (unknown ref → empty)", got)
	}
}

func TestRenderConcurrencyKey_MissingInput_StillReturnsNonEmptyKey(t *testing.T) {
	// Subtle but important: if a template has SOME literal text AND
	// a missing reference, the result is NOT empty — so it still
	// gates. Only a fully-empty template means "no gate". Pin the
	// distinction.
	got := renderConcurrencyKey(context.Background(),
		"static-prefix-{{ inputs.missing }}",
		nil,
	)
	if got == "" {
		t.Error("got empty key from non-empty template; gate would be erroneously disabled")
	}
	if got != "static-prefix-" {
		t.Errorf("got %q, want \"static-prefix-\"", got)
	}
}

func TestRenderConcurrencyKey_NumericInputStringified(t *testing.T) {
	// stringify() handles non-string values. A regression that broke
	// the json.Marshal fallback would render numbers as "0" or "<nil>",
	// gating unrelated runs together.
	got := renderConcurrencyKey(context.Background(),
		"shard-{{ inputs.shard_id }}",
		map[string]any{"shard_id": 42},
	)
	if got != "shard-42" {
		t.Errorf("got %q, want \"shard-42\" (numeric input must stringify)", got)
	}
}

func TestRenderConcurrencyKey_BoolInputStringified(t *testing.T) {
	// Bool inputs are valid DSL types; their string form (true/false)
	// is a legitimate gate token (e.g. "fast-path-true" vs "fast-path-false").
	got := renderConcurrencyKey(context.Background(),
		"fast-{{ inputs.fast_path }}",
		map[string]any{"fast_path": true},
	)
	if got != "fast-true" {
		t.Errorf("got %q, want \"fast-true\"", got)
	}
}

func TestRenderConcurrencyKey_DisallowedNamespaces_RenderEmpty(t *testing.T) {
	// Source comment: "We only support `{{ inputs.X }}` here — the
	// full Render pipeline isn't reachable yet (no step outputs at
	// reservation time)". Pin that {{ steps.X.output }} and
	// {{ env.RUN_ID }} render to empty here, otherwise a regression
	// would tie reservation to data that doesn't exist yet.
	got := renderConcurrencyKey(context.Background(),
		"k-{{ steps.s1.output }}-{{ env.RUN_ID }}",
		map[string]any{},
	)
	// Both references render empty; the literal prefix and dash remain.
	if got != "k--" {
		t.Errorf("got %q, want \"k--\" (steps and env unsupported at reservation time)", got)
	}
}

func TestRenderConcurrencyKey_NestedInputPath(t *testing.T) {
	// `inputs.X.Y` traversal: when X is map[string]any, look up Y.
	// Useful for templated keys like {{ inputs.repo.owner }}.
	got := renderConcurrencyKey(context.Background(),
		"by-{{ inputs.repo.owner }}",
		map[string]any{"repo": map[string]any{"owner": "alice", "name": "tool"}},
	)
	if got != "by-alice" {
		t.Errorf("got %q, want \"by-alice\" (one-level nesting must resolve)", got)
	}
}

func TestRenderConcurrencyKey_WhitespaceTolerant(t *testing.T) {
	// strings.TrimSpace on the template body — `{{ inputs.x }}` and
	// `{{inputs.x}}` and `{{   inputs.x   }}` must all behave the same.
	// A regression that broke trimming would intermittently miss-render
	// templates depending on whitespace style.
	cases := []string{
		"{{inputs.x}}",
		"{{ inputs.x }}",
		"{{   inputs.x   }}",
		"{{\tinputs.x\t}}",
	}
	for _, tmpl := range cases {
		t.Run(tmpl, func(t *testing.T) {
			got := renderConcurrencyKey(context.Background(), tmpl, map[string]any{"x": "v"})
			if got != "v" {
				t.Errorf("template %q rendered to %q, want \"v\"", tmpl, got)
			}
		})
	}
}

func TestRenderConcurrencyKey_NilInputsMap(t *testing.T) {
	// Defensive: a non-empty template with nil inputs must not panic.
	// All references resolve to empty. Important because the executor
	// builds the inputs map lazily; a nil map could slip through if
	// the DSL has no declared inputs.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderConcurrencyKey panicked with nil inputs: %v", r)
		}
	}()
	got := renderConcurrencyKey(context.Background(), "k-{{ inputs.x }}", nil)
	if got != "k-" {
		t.Errorf("got %q, want \"k-\" (nil inputs → empty resolution)", got)
	}
}
