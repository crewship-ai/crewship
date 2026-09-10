package pipeline

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestNestedInputsTypedReferences(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		value      any
	}{
		{"zero", "integer", float64(0)},
		{"precise integer", "integer", int64(9007199254740993)},
		{"false", "boolean", false},
		{"list", "array", []any{"a", false}},
		{"object", "object", map[string]any{"message": "{{ inputs.secret }}"}},
		{"null", "object", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderNestedInputs(map[string]any{"target": "{{ inputs.source }}"}, []InputSpec{{Name: "target", Type: tc.kind}}, RenderContext{Inputs: map[string]any{"source": tc.value}})
			if err != nil || !reflect.DeepEqual(got["target"], tc.value) {
				t.Fatalf("got %#v, err %v", got, err)
			}
		})
	}
	got, err := renderNestedInputs(map[string]any{"target": "{{ steps.fetch.output.items }}"}, []InputSpec{{Name: "target", Type: "array"}}, RenderContext{StepOutputs: map[string]string{"fetch": `{"items":[0,false]}`}})
	if err != nil || !reflect.DeepEqual(got["target"], []any{float64(0), false}) {
		t.Fatalf("projected array: %#v %v", got, err)
	}
}

func TestN8MissingOptionalNestedInput(t *testing.T) {
	for _, kind := range []string{"object", "array", "integer", "number", "boolean"} {
		t.Run(kind, func(t *testing.T) {
			got, err := renderNestedInputs(map[string]any{"target": "{{ inputs.missing }}"}, []InputSpec{{Name: "target", Type: kind}}, RenderContext{})
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := got["target"]; exists {
				t.Fatalf("missing input must remain omitted: %#v", got)
			}
		})
	}
}

func TestNestedInputsStepJSONNullAndFences(t *testing.T) {
	for _, tc := range []struct {
		ref, raw, kind string
		want           any
	}{
		{"{{ steps.fetch.output.items }}", `{"items":null}`, "object", nil},
		{"{{ steps.fetch.output }}", "```json\n[0,false]\n```", "array", []any{float64(0), false}},
	} {
		got, err := renderNestedInputs(map[string]any{"target": tc.ref}, []InputSpec{{Name: "target", Type: tc.kind}}, RenderContext{StepOutputs: map[string]string{"fetch": tc.raw}})
		if err != nil || !reflect.DeepEqual(got["target"], tc.want) {
			t.Fatalf("step value: %#v %v", got, err)
		}
	}
}

func TestNestedInputsRejectInvalidBindingsWithoutLeakingValues(t *testing.T) {
	for _, tc := range []struct {
		ref, kind string
		render    RenderContext
	}{
		{"{{ inputs.missing }}", "integer", RenderContext{}},
		{"{{ steps.fetch.output.items }}", "array", RenderContext{StepOutputs: map[string]string{"fetch": `{}`}}},
		{"{{ inputs.value }}", "integer", RenderContext{Inputs: map[string]any{"value": 1.5}}},
		{"{{ inputs.value }}", "boolean", RenderContext{Inputs: map[string]any{"value": "private-secret"}}},
		{"{{ inputs.value }}", "object", RenderContext{Inputs: map[string]any{"value": nil}}},
		{"{{ inputs.value }}", "array", RenderContext{Inputs: map[string]any{"value": []any{"private-secret"}}, UntrustedInputs: map[string]struct{}{"value": {}}}},
	} {
		_, err := renderNestedInputs(map[string]any{"target": tc.ref}, []InputSpec{{Name: "target", Type: tc.kind, Required: true}}, tc.render)
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("unsafe binding error: %v", err)
		}
	}
}

func TestNestedInputsKeepLegacyTextAndLiteralMaps(t *testing.T) {
	values := map[string]any{"text": "{{ inputs.zero }}", "undeclared": "{{ inputs.zero }}", "mixed": "value={{ inputs.zero }}", "literal": map[string]any{"text": "{{ inputs.zero }}"}}
	got, err := renderNestedInputs(values, []InputSpec{{Name: "text", Type: "string"}, {Name: "mixed", Type: "integer"}}, RenderContext{Inputs: map[string]any{"zero": 0}})
	if err != nil || got["text"] != "0" || got["undeclared"] != "0" || got["mixed"] != "value=0" || !reflect.DeepEqual(got["literal"], values["literal"]) {
		t.Fatalf("legacy behavior changed: %#v %v", got, err)
	}
}

func TestExecutorCallPipelineTypedInputsBeforeDispatch(t *testing.T) {
	for _, valid := range []bool{true, false} {
		store, resolver, cleanup := openExecutorTestDB(t)
		runner := newMockRunner()
		child := fakePipeline(t, "inner", `{"name":"inner","inputs":[{"name":"count","type":"integer","widget":"number","required":true},{"name":"enabled","type":"boolean","widget":"boolean","required":true}],"steps":[{"id":"x","type":"agent_run","agent_slug":"agent_lead","prompt":"count={{ inputs.count }} enabled={{ inputs.enabled }}"}]}`, "crew_a", "agent_lead")
		exec := NewExecutor(store, resolver, runner, &captureEmitter{}).WithPipelineResolver(pipeResolverFn(func(context.Context, string, string) (*Pipeline, error) { return child, nil }))
		var count any = float64(0)
		if !valid {
			count = "invalid"
		}
		outer := &DSL{Name: "outer", Inputs: []InputSpec{{Name: "count", Type: "integer"}, {Name: "enabled", Type: "boolean"}}, Steps: []Step{{ID: "call", Type: StepCallPipeline, PipelineSlug: "inner", NestedInputs: map[string]any{"count": "{{ inputs.count }}", "enabled": "{{ inputs.enabled }}"}}}}
		result, err := exec.RunDefinition(context.Background(), outer, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun, Inputs: map[string]any{"count": count, "enabled": false}})
		if err != nil {
			t.Fatal(err)
		}
		if valid {
			if result.Status != "COMPLETED" || len(runner.calls) != 1 || runner.calls[0].Prompt != "count=0 enabled=false" {
				t.Fatalf("typed child failed: %+v calls %+v", result, runner.calls)
			}
		} else if result.Status != "FAILED" || len(runner.calls) != 0 {
			t.Fatalf("invalid child dispatched: %+v calls %+v", result, runner.calls)
		}
		cleanup()
	}
}
