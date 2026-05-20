package pipeline

import (
	"context"
	"errors"
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
// deadlocks; a regression that returned "no gate" for a non-empty
// template whose inputs went missing would allow unlimited parallelism
// for a routine the author asked us to serialise.
// ---------------------------------------------------------------------------

func TestRenderConcurrencyKey_EmptyTemplate_ReturnsEmpty(t *testing.T) {
	// Empty template MUST yield empty key + gated=false — the executor takes
	// that as "no concurrency gate" and dispatches without locking.
	got, gated, err := renderConcurrencyKey(context.Background(), "", map[string]any{"x": "y"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" || gated {
		t.Errorf("renderConcurrencyKey(\"\") = (%q, %v), want (\"\", false)", got, gated)
	}
}

func TestRenderConcurrencyKey_EmptyTemplate_NilInputs(t *testing.T) {
	// Nil inputs must NOT cause the empty-template fast-path to throw —
	// reservation-time callers may pass nil before they've evaluated
	// step inputs.
	got, gated, err := renderConcurrencyKey(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" || gated {
		t.Errorf("nil inputs with empty template = (%q, %v), want (\"\", false)", got, gated)
	}
}

func TestRenderConcurrencyKey_LiteralTemplate_NoSubstitutions(t *testing.T) {
	// A template with no {{ ... }} substitutions passes through. Pin
	// so a future "treat every template as a key prefix" change has to
	// flip this test deliberately rather than silently breaking the
	// "static key" pattern (e.g. concurrency_key: "build-queue").
	got, gated, err := renderConcurrencyKey(context.Background(), "build-queue", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "build-queue" || !gated {
		t.Errorf("literal template = (%q, %v), want (\"build-queue\", true)", got, gated)
	}
}

func TestRenderConcurrencyKey_SubstitutesSingleInput(t *testing.T) {
	got, _, err := renderConcurrencyKey(context.Background(),
		"deploy-{{ inputs.env }}",
		map[string]any{"env": "production"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deploy-production" {
		t.Errorf("got %q, want \"deploy-production\"", got)
	}
}

func TestRenderConcurrencyKey_SubstitutesMultipleInputs(t *testing.T) {
	got, _, err := renderConcurrencyKey(context.Background(),
		"{{ inputs.project }}-{{ inputs.env }}",
		map[string]any{"project": "alpha", "env": "staging"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "alpha-staging" {
		t.Errorf("got %q, want \"alpha-staging\"", got)
	}
}

func TestRenderConcurrencyKey_MissingInput_RendersPrefixOnly(t *testing.T) {
	// "Unknown references render as empty strings". The literal prefix
	// keeps the key non-empty so the gate still engages. Pin so a
	// regression to "panic on missing" or "leave the {{...}} marker"
	// surfaces immediately.
	got, gated, err := renderConcurrencyKey(context.Background(),
		"deploy-{{ inputs.missing }}",
		map[string]any{"present": "x"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deploy-" || !gated {
		t.Errorf("missing-input render = (%q, %v), want (\"deploy-\", true)", got, gated)
	}
}

func TestRenderConcurrencyKey_AllReferencesMissing_FailsFast(t *testing.T) {
	// THE bug this fix addresses. A template like `{{ inputs.account_id }}`
	// with no literal text + a missing input renders to "" — pre-fix the
	// executor would interpret that as "no gate" and allow unlimited
	// parallelism for a routine the author explicitly asked us to
	// serialise. Must return ErrConcurrencyKeyEmpty so the run fails
	// fast with a clear "your referenced input is missing" message.
	got, gated, err := renderConcurrencyKey(context.Background(),
		"{{ inputs.account_id }}",
		map[string]any{}, // account_id intentionally absent
	)
	if !errors.Is(err, ErrConcurrencyKeyEmpty) {
		t.Fatalf("got err=%v gated=%v key=%q, want ErrConcurrencyKeyEmpty", err, gated, got)
	}
	if got != "" {
		t.Errorf("got key=%q on failure, want empty (caller must not gate on stale value)", got)
	}
	if !gated {
		t.Error("gated=false on author-intended gate; caller would skip the error and bypass")
	}
}

func TestRenderConcurrencyKey_EmptyStringInput_FailsFast(t *testing.T) {
	// Adjacent failure mode: the input IS present but its value is "".
	// Same outcome — an empty rendered key cannot gate, so fail fast
	// rather than silently allow parallelism.
	_, _, err := renderConcurrencyKey(context.Background(),
		"{{ inputs.account_id }}",
		map[string]any{"account_id": ""},
	)
	if !errors.Is(err, ErrConcurrencyKeyEmpty) {
		t.Fatalf("got err=%v, want ErrConcurrencyKeyEmpty", err)
	}
}

func TestRenderConcurrencyKey_MissingInput_WithLiteralPrefix_StillGates(t *testing.T) {
	// Subtle but important: if a template has SOME literal text AND
	// a missing reference, the result is NOT empty — so it still
	// gates. Only a fully-empty *rendered* result fails fast. Pin the
	// distinction.
	got, gated, err := renderConcurrencyKey(context.Background(),
		"static-prefix-{{ inputs.missing }}",
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "static-prefix-" || !gated {
		t.Errorf("got (%q, %v), want (\"static-prefix-\", true)", got, gated)
	}
}

func TestRenderConcurrencyKey_NumericInputStringified(t *testing.T) {
	got, _, err := renderConcurrencyKey(context.Background(),
		"shard-{{ inputs.shard_id }}",
		map[string]any{"shard_id": 42},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "shard-42" {
		t.Errorf("got %q, want \"shard-42\" (numeric input must stringify)", got)
	}
}

func TestRenderConcurrencyKey_BoolInputStringified(t *testing.T) {
	got, _, err := renderConcurrencyKey(context.Background(),
		"fast-{{ inputs.fast_path }}",
		map[string]any{"fast_path": true},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "fast-true" {
		t.Errorf("got %q, want \"fast-true\"", got)
	}
}

func TestRenderConcurrencyKey_DisallowedNamespaces_RenderEmpty(t *testing.T) {
	// `{{ steps.X.output }}` and `{{ env.RUN_ID }}` render to empty here
	// (unsupported at reservation time). With the literal `k-` prefix and
	// dashes the key is still non-empty, so the gate engages.
	got, _, err := renderConcurrencyKey(context.Background(),
		"k-{{ steps.s1.output }}-{{ env.RUN_ID }}",
		map[string]any{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "k--" {
		t.Errorf("got %q, want \"k--\" (steps and env unsupported at reservation time)", got)
	}
}

func TestRenderConcurrencyKey_NestedInputPath(t *testing.T) {
	got, _, err := renderConcurrencyKey(context.Background(),
		"by-{{ inputs.repo.owner }}",
		map[string]any{"repo": map[string]any{"owner": "alice", "name": "tool"}},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "by-alice" {
		t.Errorf("got %q, want \"by-alice\" (one-level nesting must resolve)", got)
	}
}

func TestRenderConcurrencyKey_WhitespaceTolerant(t *testing.T) {
	cases := []string{
		"{{inputs.x}}",
		"{{ inputs.x }}",
		"{{   inputs.x   }}",
		"{{\tinputs.x\t}}",
	}
	for _, tmpl := range cases {
		t.Run(tmpl, func(t *testing.T) {
			got, _, err := renderConcurrencyKey(context.Background(), tmpl, map[string]any{"x": "v"})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != "v" {
				t.Errorf("template %q rendered to %q, want \"v\"", tmpl, got)
			}
		})
	}
}

func TestRenderConcurrencyKey_NilInputsMap_AllMissing_FailsFast(t *testing.T) {
	// Defensive: a non-empty template with nil inputs must not panic.
	// All references resolve to empty; if there's no literal text, the
	// rendered key is "" and we fail fast (post-fix behaviour). Pre-fix
	// this case silently allowed unlimited parallelism.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("renderConcurrencyKey panicked with nil inputs: %v", r)
		}
	}()
	_, _, err := renderConcurrencyKey(context.Background(), "{{ inputs.x }}", nil)
	if !errors.Is(err, ErrConcurrencyKeyEmpty) {
		t.Fatalf("got err=%v, want ErrConcurrencyKeyEmpty", err)
	}
}

func TestRenderConcurrencyKey_NilInputsMap_WithLiteral_Succeeds(t *testing.T) {
	// Same nil-inputs defensive case as above but WITH a literal prefix:
	// the rendered key is "k-" which is non-empty → gate engaged, no
	// error. Companion to the test above so the distinction stays pinned.
	got, gated, err := renderConcurrencyKey(context.Background(), "k-{{ inputs.x }}", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "k-" || !gated {
		t.Errorf("got (%q, %v), want (\"k-\", true)", got, gated)
	}
}

func TestRenderConcurrencyKey_AppliedAfterMergeDefaults(t *testing.T) {
	// The fail-fast must NOT trip when the input the key references
	// is satisfied by an InputSpec default rather than the raw caller
	// payload. The bug this guards: executor.Run originally rendered
	// the key against `in.Inputs` (raw) before runDSL did its
	// mergeInputs pass, so a routine declaring
	//
	//   inputs:           [{name: "tenant", default: "global"}]
	//   concurrency_key:  "{{ inputs.tenant }}"
	//
	// would falsely return ErrConcurrencyKeyEmpty when the caller
	// POST'd `{"inputs":{}}` — defeating the entire point of having
	// a default. The fix in executor.go calls mergeInputs first;
	// this test pins that the merged-map path resolves correctly.
	dsl := &DSL{
		Inputs:         []InputSpec{{Name: "tenant", Type: "string", Default: "global"}},
		ConcurrencyKey: "{{ inputs.tenant }}",
	}
	merged := mergeInputs(map[string]any{}, dsl) // caller passed nothing
	got, gated, err := renderConcurrencyKey(context.Background(), dsl.ConcurrencyKey, merged)
	if err != nil {
		t.Fatalf("unexpected error after merge: %v", err)
	}
	if got != "global" || !gated {
		t.Errorf("got (%q, %v), want (\"global\", true) — default should have flowed through", got, gated)
	}
}

func TestRenderConcurrencyKey_FailsFastEvenAfterMerge_WhenNoDefault(t *testing.T) {
	// Inverse companion: the routine declares the input but DOES NOT
	// give it a default, and the caller omits it. After merge, the
	// map is still {} → render to "" → fail fast. Pin so the fix
	// doesn't accidentally introduce a phantom default like "" or
	// "<missing>" that re-opens the silent-bypass class.
	dsl := &DSL{
		Inputs:         []InputSpec{{Name: "tenant", Type: "string"}}, // no default
		ConcurrencyKey: "{{ inputs.tenant }}",
	}
	merged := mergeInputs(map[string]any{}, dsl)
	_, _, err := renderConcurrencyKey(context.Background(), dsl.ConcurrencyKey, merged)
	if !errors.Is(err, ErrConcurrencyKeyEmpty) {
		t.Fatalf("got err=%v, want ErrConcurrencyKeyEmpty (no default → still empty after merge)", err)
	}
}
