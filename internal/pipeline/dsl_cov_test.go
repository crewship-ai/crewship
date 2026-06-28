package pipeline

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dsl.go — Parse error path, Validate top-level branches, DAG-mode
// template validation, CycleDetect, resolveRef / stringify / jsonPath /
// walkNestedTemplates render helpers, checkTemplateRef namespaces.
// ---------------------------------------------------------------------------

func TestParse_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("{not json"))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse DSL") {
		t.Errorf("error %q should mention parse DSL", err)
	}
}

func TestValidate_TopLevelBranches(t *testing.T) {
	t.Parallel()

	httpStep := Step{ID: "s1", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://example.com"}}

	cases := []struct {
		name    string
		dsl     *DSL
		wantErr string
	}{
		{"nil dsl", nil, "nil DSL"},
		{"unsupported version", &DSL{DSLVersion: "9.9", Name: "x", Steps: []Step{httpStep}}, "unsupported DSL version"},
		{"missing name", &DSL{Steps: []Step{httpStep}}, "name required"},
		{"bad name shape", &DSL{Name: "Bad Name!", Steps: []Step{httpStep}}, "kebab-case"},
		{"no steps", &DSL{Name: "ok-name"}, "at least one step required"},
		{"valid", &DSL{DSLVersion: "1.0", Name: "ok-name", Steps: []Step{httpStep}}, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.dsl, nil, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want fragment %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidate_DAGTemplateResolution covers the DAG-mode template
// validator: needs-graph errors surface at save, out-of-source-order
// references to declared ancestors pass, undeclared references fail.
func TestValidate_DAGTemplateResolution(t *testing.T) {
	t.Parallel()

	mk := func(id string, needs []string, url string) Step {
		return Step{ID: id, Type: StepHTTP, Needs: needs,
			HTTP: &HTTPStep{Method: "GET", URL: url}}
	}

	// Ghost needs entry → validateDAG error wrapped by validateTemplates.
	ghost := &DSL{Name: "dag-ghost", Steps: []Step{
		mk("b", []string{"ghost"}, "https://x"),
	}}
	if err := Validate(ghost, nil, nil); err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Errorf("ghost needs: got %v", err)
	}

	// Out-of-source-order reference to a declared ancestor must PASS:
	// b is listed before a, but needs a, so {{ steps.a.output }} is legal.
	ok := &DSL{Name: "dag-ok", Steps: []Step{
		mk("b", []string{"a"}, "https://x/{{ steps.a.output }}"),
		mk("a", nil, "https://x"),
	}}
	if err := Validate(ok, nil, nil); err != nil {
		t.Errorf("declared ancestor ref should pass, got %v", err)
	}

	// Reference to a step OUTSIDE the needs closure must fail.
	bad := &DSL{Name: "dag-bad", Steps: []Step{
		mk("a", nil, "https://x"),
		mk("b", []string{"a"}, "https://x"),
		mk("c", []string{"a"}, "https://x/{{ steps.b.output }}"), // b not an ancestor of c
	}}
	if err := Validate(bad, nil, nil); err == nil || !strings.Contains(err.Error(), "hasn't run yet") {
		t.Errorf("non-ancestor ref: got %v", err)
	}
}

func TestCollectReachableNeeds(t *testing.T) {
	t.Parallel()
	steps := map[string]*Step{
		"a": {ID: "a"},
		"b": {ID: "b", Needs: []string{"a"}},
		"c": {ID: "c", Needs: []string{"b", "a", "missing"}}, // dup + unknown deps
	}
	out := map[string]struct{}{}
	collectReachableNeeds("c", steps, out)
	if _, ok := out["a"]; !ok {
		t.Error("transitive ancestor a missing")
	}
	if _, ok := out["b"]; !ok {
		t.Error("direct ancestor b missing")
	}
	if _, ok := out["missing"]; ok {
		t.Error("unknown dep must not be collected")
	}
	// Unknown root: no-op, no panic.
	out2 := map[string]struct{}{}
	collectReachableNeeds("nope", steps, out2)
	if len(out2) != 0 {
		t.Errorf("unknown root collected %v", out2)
	}
}

// TestValidate_TemplateFieldsBreadth pins that templates inside wait /
// code / transform step fields are validated, not just prompts.
func TestValidate_TemplateFieldsBreadth(t *testing.T) {
	t.Parallel()

	badRef := "{{ inputs.nope }}"
	cases := []struct {
		name string
		step Step
	}{
		{"wait until", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "datetime", Until: badRef}}},
		{"wait event filter", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "event", EventType: "e", EventFilter: badRef}}},
		{"wait approval prompt", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "approval", ApprovalPrompt: badRef}}},
		{"code body", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "echo " + badRef}}},
		{"code env value", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "echo hi", Env: map[string]string{"X": badRef}}}},
		{"transform input", Step{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: badRef, Expression: ".x"}}},
		{"transform expression", Step{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: "in", Expression: badRef}}},
		{"http header value", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x", Headers: map[string]string{"X-H": badRef}}}},
		{"http body", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x", Body: badRef}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dsl := &DSL{Name: "tmpl-breadth", Steps: []Step{tc.step}}
			err := Validate(dsl, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "unknown input") {
				t.Errorf("expected unknown-input error, got %v", err)
			}
		})
	}
}

func TestCheckTemplateRef_Namespaces(t *testing.T) {
	t.Parallel()
	inputs := map[string]struct{}{"since": {}}
	earlier := map[string]struct{}{"fetch": {}}

	cases := []struct {
		ref     string
		wantErr string
	}{
		{"justone", "invalid template ref"},
		{"inputs.since", ""},
		{"inputs.since.deep.path", ""},
		{"inputs.nope", "unknown input"},
		{"steps.fetch", "expected steps.Y.output"},
		{"steps.fetch.output", ""},
		{"steps.later.output", "hasn't run yet"},
		{"env.run_id", ""}, // env allowlist enforced at render time
		{"globals.x", "unknown namespace"},
	}
	for _, tc := range cases {
		err := checkTemplateRef(tc.ref, inputs, earlier)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("ref %q: expected nil, got %v", tc.ref, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("ref %q: got %v, want fragment %q", tc.ref, err, tc.wantErr)
		}
	}
}

func TestCycleDetect(t *testing.T) {
	t.Parallel()

	// nil DSL → nil.
	if err := CycleDetect(nil, nil); err != nil {
		t.Errorf("nil dsl: %v", err)
	}

	a := &DSL{Name: "a", Steps: []Step{{ID: "s", Type: StepCallPipeline, PipelineSlug: "b"}}}
	b := &DSL{Name: "b", Steps: []Step{{ID: "s", Type: StepCallPipeline, PipelineSlug: "a"}}}
	shared := &DSL{Name: "shared", Steps: []Step{{ID: "s", Type: StepHTTP}}}
	diamondTop := &DSL{Name: "top", Steps: []Step{
		{ID: "s1", Type: StepCallPipeline, PipelineSlug: "left"},
		{ID: "s2", Type: StepCallPipeline, PipelineSlug: "right"},
	}}
	left := &DSL{Name: "left", Steps: []Step{{ID: "s", Type: StepCallPipeline, PipelineSlug: "shared"}}}
	right := &DSL{Name: "right", Steps: []Step{{ID: "s", Type: StepCallPipeline, PipelineSlug: "shared"}}}

	registry := map[string]*DSL{"a": a, "b": b, "shared": shared, "left": left, "right": right, "top": diamondTop}
	resolve := func(slug string) (*DSL, error) {
		if d, ok := registry[slug]; ok {
			return d, nil
		}
		return nil, errors.New("not found")
	}

	// A→B→A cycle detected.
	if err := CycleDetect(a, resolve); err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("a→b→a: got %v", err)
	}

	// Diamond (top→left→shared, top→right→shared) is NOT a cycle; the
	// second visit of "shared" hits the visited short-circuit.
	if err := CycleDetect(diamondTop, resolve); err != nil {
		t.Errorf("diamond: %v", err)
	}

	// Unknown target → skipped, no false positive.
	unknown := &DSL{Name: "u", Steps: []Step{{ID: "s", Type: StepCallPipeline, PipelineSlug: "ghost"}}}
	if err := CycleDetect(unknown, resolve); err != nil {
		t.Errorf("unknown target: %v", err)
	}
}

func TestResolveRef_Branches(t *testing.T) {
	t.Parallel()
	ctx := RenderContext{
		Inputs: map[string]any{
			"flat":   "v",
			"nested": map[string]any{"key": "deep", "num": 7},
			"scalar": 42,
		},
		StepOutputs: map[string]string{
			"fetch": `{"count": 3, "meta": {"page": 2}}`,
			"plain": "raw-output",
		},
		Env: map[string]string{"run_id": "r1"},
	}

	cases := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{"single", "", false},                      // <2 parts
		{"inputs.flat", "v", true},                 //
		{"inputs.missing", "", false},              //
		{"inputs.nested.key", "deep", true},        // one-level JSON path
		{"inputs.nested.missing", "", false},       // path miss
		{"inputs.scalar.key", "42", true},          // non-map with path → stringify whole
		{"steps.fetch", "", false},                 // <3 parts
		{"steps.missing.output", "", false},        //
		{"steps.plain.output", "raw-output", true}, //
		{"steps.fetch.output.count", "3", true},    // json path into output
		{"steps.fetch.outputs", "", false},         // wrong suffix
		{"env.run_id", "r1", true},                 //
		{"env.secret", "", false},                  // not in safe env
		{"globals.x", "", false},                   // unknown namespace
	}
	for _, tc := range cases {
		got, ok := resolveRef(tc.ref, ctx)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("resolveRef(%q) = (%q, %v), want (%q, %v)", tc.ref, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestStringify(t *testing.T) {
	t.Parallel()
	if got := stringify(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := stringify("s"); got != "s" {
		t.Errorf("string: %q", got)
	}
	if got := stringify(3.5); got != "3.5" {
		t.Errorf("float: %q", got)
	}
	if got := stringify(map[string]any{"a": 1}); got != `{"a":1}` {
		t.Errorf("map: %q", got)
	}
	// Unmarshalable type falls back to %v formatting.
	ch := make(chan int)
	if got := stringify(ch); !strings.HasPrefix(got, "0x") {
		t.Errorf("chan fallback: %q", got)
	}
}

func TestJSONPath(t *testing.T) {
	t.Parallel()
	if got := jsonPath("not json at all {{", "a"); got != "" {
		t.Errorf("decode failure: %q", got)
	}
	if got := jsonPath(`{"a":{"b":"x"}}`, "a.b"); got != "x" {
		t.Errorf("nested: %q", got)
	}
	if got := jsonPath(`{"a":"scalar"}`, "a.b"); got != "" {
		t.Errorf("descend into scalar: %q", got)
	}
	if got := jsonPath(`{"a":1}`, "missing"); got != "" {
		t.Errorf("missing key: %q", got)
	}
}

func TestWalkNestedTemplates(t *testing.T) {
	t.Parallel()
	var seen []string
	walk := func(s string) error {
		seen = append(seen, s)
		if s == "BAD" {
			return errors.New("bad template")
		}
		return nil
	}

	// nil / scalar non-strings are skipped.
	if err := walkNestedTemplates(nil, walk); err != nil {
		t.Errorf("nil: %v", err)
	}
	if err := walkNestedTemplates(42, walk); err != nil {
		t.Errorf("int: %v", err)
	}

	// Deep nesting: map → slice → string.
	v := map[string]any{
		"a": "one",
		"b": []any{"two", map[string]any{"c": "three"}, 7},
	}
	if err := walkNestedTemplates(v, walk); err != nil {
		t.Fatalf("nested: %v", err)
	}
	if len(seen) != 3 {
		t.Errorf("expected 3 strings walked, got %v", seen)
	}

	// Error propagation from inside a slice.
	if err := walkNestedTemplates([]any{"ok", "BAD"}, walk); err == nil {
		t.Error("expected propagated error from slice element")
	}
	// Error propagation from inside a map.
	if err := walkNestedTemplates(map[string]any{"k": "BAD"}, walk); err == nil {
		t.Error("expected propagated error from map value")
	}
}

func TestRender_UnknownRefRendersEmpty_Cov(t *testing.T) {
	t.Parallel()
	out := Render("a {{ inputs.miss }} b {{ env.run_id }}", RenderContext{
		Env: map[string]string{"run_id": "r9"},
	})
	if out != "a  b r9" {
		t.Errorf("got %q", out)
	}
}
