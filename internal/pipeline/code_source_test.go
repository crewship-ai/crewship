package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

func TestCodeSourceCompatibility(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`inputs.value > 5`, `inputs.value > 5`},
		{`{{ inputs.x }} > {{ inputs.y }}`, `inputs["x"] > inputs["y"]`},
		{`"{{ inputs.x }}" == "approved"`, `inputs["x"] == "approved"`},
		{`'{{ steps.tool-a.output.value }}'`, `steps["tool-a"]["output"]["value"]`},
		{`{{ inputs.0 }}`, `inputs["0"]`},
		{`{{ inputs.část }}`, `inputs["část"]`},
		{`r'raw\' == r'raw\' && {{ inputs.x }}`, `r'raw\' == r'raw\' && inputs["x"]`},
		{`"""ordinary""" + string({{ inputs.x }})`, `"""ordinary""" + string(inputs["x"])`},
		{"// author's {{ inputs.x }}\ntrue", "// author's inputs[\"x\"]\ntrue"},
	} {
		got, err := normalizeCodeSource(tc.source)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q err=%v want %q", tc.source, got, err, tc.want)
		}
		second, err := normalizeCodeSource(got)
		if err != nil || second != got {
			t.Fatalf("not idempotent: %q err=%v", second, err)
		}
	}
	for _, source := range []string{
		`"prefix {{ inputs.x }}"`, `"{{ inputs.x }} suffix"`,
		`{{ inputs.x + 1 }}`, `{{ inputs.x() }}`, `{{ inputs..x }}`, `{{ inputs.x`,
		`r"{{ inputs.x }}"`, `b"{{ inputs.x }}"`, `"""{{ inputs.x }}"""`,
	} {
		if _, err := normalizeCodeSource(source); err == nil || !strings.Contains(err.Error(), "typed") {
			t.Fatalf("%q must fail with typed-variable guidance: %v", source, err)
		}
	}
}

func TestCodeTypedOutputsAndMapOperations(t *testing.T) {
	for _, tc := range []struct{ output, code, want string }{
		{`7`, `steps.a.output == 7`, "true"},
		{`"7"`, `steps.a.output == "7"`, "true"},
		{`true`, `steps.a.output == true`, "true"},
		{`null`, `steps.a.output == null`, "true"},
		{`7 trailing`, `steps.a.output == "7 trailing"`, "true"},
		{`{"x":7}`, `steps.a.x == 7 && steps.a.output.x == 7`, "true"},
		{`{"output":"field","x":7}`, `steps.a.output.output == "field" && steps.a.x == 7`, "true"},
		{`7`, `size(steps) == 1 && "a" in steps && has(steps.a.output)`, "true"},
		{`7`, `steps == {"a":{"output":7}}`, "true"},
		{`7`, `{"a":{"output":7}} == steps`, "true"},
		{`7`, `steps.all(k, steps[k].output > 0)`, "true"},
		{`7`, `type(steps) == map`, "true"},
		{`{"nested":{"x":7}}`, `{{ steps.a.output.nested.x }} == 7`, "true"},
	} {
		variables := codeContextVariables(RenderContext{StepOutputs: map[string]string{"a": tc.output}})
		got, err := (CelCodeRunner{}).RunCode(context.Background(), CodeRunRequest{Runtime: "cel", Code: tc.code, Variables: variables})
		if err != nil || got.Stdout != tc.want {
			t.Fatalf("output=%q code=%q got=%+v err=%v", tc.output, tc.code, got, err)
		}
	}
}

func TestCodeTypedDataNeverBecomesSource(t *testing.T) {
	for _, runtime := range []string{"cel", "expr"} {
		for _, value := range []string{`x" == "x" || true || "x`, `"]["other"]`, `{{ inputs.other }}`, "ſK\xff"} {
			step := Step{ID: "compare", Type: StepCode, Code: &CodeStep{Runtime: runtime, Code: `"{{ steps.tool-a.output }}" == "approved"`}}
			before, _ := json.Marshal(step)
			executor := (&Executor{}).WithCodeRunner(NewMultiCodeRunner())
			got, _, _, err := executor.runCodeStep(context.Background(), step, RenderContext{StepOutputs: map[string]string{"tool-a": value}}, RunInput{WorkspaceID: "ws_test"})
			if err != nil || got != "false" {
				t.Fatalf("runtime=%s value=%q output=%q err=%v", runtime, value, got, err)
			}
			after, _ := json.Marshal(step)
			if string(before) != string(after) {
				t.Fatal("runtime rewrote definition")
			}
		}
	}
}

func TestCodeMissingAndComplexBindingsFailClosed(t *testing.T) {
	for _, runtime := range []string{"cel", "expr"} {
		for _, code := range []string{`{{ inputs.missing }} == ""`, `{{ steps.missing.output }} == ""`, `{{ secrets.missing }} == ""`, `"x {{ inputs.value }}" == "x data"`} {
			_, err := NewMultiCodeRunner().RunCode(context.Background(), CodeRunRequest{Runtime: runtime, Code: code, Inputs: map[string]any{"value": "data"}})
			if err == nil {
				t.Fatalf("runtime=%s code=%q unexpectedly succeeded", runtime, code)
			}
		}
	}
}

func TestCodeExprSelectorsAndQuotedOperators(t *testing.T) {
	for _, code := range []string{`inputs["x>y"] == "a>=b"`, `{{ inputs.x-y }} == "a>=b"`, `inputs.array[0] == "a>=b"`} {
		result, err := (ExprCodeRunner{}).RunCode(context.Background(), CodeRunRequest{Runtime: "expr", Code: code,
			Inputs:   map[string]any{"x>y": "a>=b", "x-y": "a>=b", "array": []any{"a>=b"}},
			InputEnv: map[string]string{`inputs["x>y"]`: "different"}})
		if err != nil || result.Stdout != "true" {
			t.Fatalf("code=%q got=%+v err=%v", code, result, err)
		}
	}
	for _, code := range []string{`1 > 0 == true`, `inputs.x + 1 > 0`, `inputs.x[ == ""`} {
		if _, err := (ExprCodeRunner{}).RunCode(context.Background(), CodeRunRequest{Runtime: "expr", Code: code}); err == nil {
			t.Fatalf("compound/malformed expression succeeded: %s", code)
		}
	}
}

func TestCodeLegacySecretRetainsScopedResolutionAndScrub(t *testing.T) {
	for _, runtime := range []string{"cel", "expr"} {
		executor := (&Executor{}).WithCodeRunner(NewMultiCodeRunner()).WithCredentialResolver(fakeSecretResolver)
		code := `{{ secrets.stripe }}`
		if runtime == "expr" {
			code += ` > 0`
		}
		step := Step{ID: "secret", Type: StepCode, Code: &CodeStep{Runtime: runtime, Code: code}}
		out, _, _, err := executor.runCodeStep(context.Background(), step, RenderContext{}, RunInput{WorkspaceID: "ws_1", AuthorCrewID: "crew_1"})
		if strings.Contains(out, stripeSecretValue) || err != nil && strings.Contains(err.Error(), stripeSecretValue) {
			t.Fatal("resolved value leaked")
		}
		if runtime == "cel" && (err != nil || out != secretRedactionMarker) {
			t.Fatalf("out=%q err=%v", out, err)
		}
		if runtime == "expr" && (err == nil || !strings.Contains(err.Error(), secretRedactionMarker)) {
			t.Fatalf("expected scrubbed numeric failure: %v", err)
		}
	}
}

func TestCodeStepMapPreservesUnknownAndError(t *testing.T) {
	m := newCodeStepMap(nil)
	for _, key := range []ref.Val{types.NewErr("synthetic error"), types.NewUnknown(1, nil)} {
		if got := m.Get(key); got != key {
			t.Fatalf("Get must propagate error/unknown, got %v", got)
		}
		got, found := m.Find(key)
		if found || got != key {
			t.Fatalf("Find must propagate error/unknown, got %v %v", got, found)
		}
	}
}

func TestCodeStepMapOnlyDecodesSelectedOutputs(t *testing.T) {
	m := newCodeStepMap(map[string]string{"small": "7", "unused": strings.Repeat("x", 1<<20)})
	for _, code := range []string{"true", "steps.small.output == 7"} {
		_, err := (CelCodeRunner{}).RunCode(context.Background(), CodeRunRequest{Runtime: "cel", Code: code, Variables: map[string]any{"steps": m}})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m.decoded["unused"]; ok {
			t.Fatal("decoded unused output")
		}
	}
	if len(m.decoded) != 1 {
		t.Fatalf("decoded %d outputs", len(m.decoded))
	}
}
