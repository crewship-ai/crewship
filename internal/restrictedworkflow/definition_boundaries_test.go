package restrictedworkflow

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestWorkflowScalarInputBoundaries(t *testing.T) {
	min, max := float64(1), float64(5)
	for _, tc := range []struct {
		name           string
		spec           pipeline.InputSpec
		value          any
		absent, denied bool
	}{
		{name: "default", spec: pipeline.InputSpec{Name: "value", Type: "string", Default: "default"}, absent: true},
		{name: "optional absent", spec: pipeline.InputSpec{Name: "value", Type: "string"}, absent: true},
		{name: "required absent", spec: pipeline.InputSpec{Name: "value", Type: "string", Required: true}, absent: true, denied: true},
		{name: "boolean false", spec: pipeline.InputSpec{Name: "value", Type: "boolean"}, value: false},
		{name: "boolean true", spec: pipeline.InputSpec{Name: "value", Type: "boolean"}, value: true},
		{name: "string is not boolean", spec: pipeline.InputSpec{Name: "value", Type: "boolean"}, value: "false", denied: true},
		{name: "nil is not text", spec: pipeline.InputSpec{Name: "value", Type: "string"}, denied: true},
		{name: "large text", spec: pipeline.InputSpec{Name: "value", Type: "string"}, value: strings.Repeat("x", 16385), denied: true},
		{name: "enum match", spec: pipeline.InputSpec{Name: "value", Type: "string", Options: []string{"first", "second"}}, value: "second"},
		{name: "enum refusal", spec: pipeline.InputSpec{Name: "value", Type: "string", Options: []string{"first"}}, value: "unknown", denied: true},
		{name: "custom enum", spec: pipeline.InputSpec{Name: "value", Type: "string", Options: []string{"first"}, AllowCustom: true}, value: "unknown"},
		{name: "integer min", spec: pipeline.InputSpec{Name: "value", Type: "integer", Min: &min, Max: &max}, value: float64(1)},
		{name: "integer max", spec: pipeline.InputSpec{Name: "value", Type: "integer", Min: &min, Max: &max}, value: float64(5)},
		{name: "below min", spec: pipeline.InputSpec{Name: "value", Type: "number", Min: &min}, value: float64(0), denied: true},
		{name: "above max", spec: pipeline.InputSpec{Name: "value", Type: "number", Max: &max}, value: float64(6), denied: true},
		{name: "fraction number", spec: pipeline.InputSpec{Name: "value", Type: "number"}, value: 1.5},
		{name: "fraction integer", spec: pipeline.InputSpec{Name: "value", Type: "integer"}, value: 1.5, denied: true},
		{name: "numeric string", spec: pipeline.InputSpec{Name: "value", Type: "number"}, value: "1", denied: true},
		{name: "NaN", spec: pipeline.InputSpec{Name: "value", Type: "number"}, value: math.NaN(), denied: true},
		{name: "positive infinity", spec: pipeline.InputSpec{Name: "value", Type: "number"}, value: math.Inf(1), denied: true},
		{name: "negative infinity", spec: pipeline.InputSpec{Name: "value", Type: "number"}, value: math.Inf(-1), denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			supplied := map[string]any{}
			if !tc.absent {
				supplied["value"] = tc.value
			}
			got, err := normalizeInputs(definition{DSL: &pipeline.DSL{Inputs: []pipeline.InputSpec{tc.spec}}}, supplied)
			if tc.denied {
				if !errors.Is(err, ErrDenied) || got != nil {
					t.Fatalf("invalid input accepted: %#v %v", got, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := supplied
			if tc.absent && tc.spec.Default != nil {
				want = map[string]any{"value": tc.spec.Default}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("normalized %#v, want %#v", got, want)
			}
		})
	}
}

func TestWorkflowInputEnvelopeLimits(t *testing.T) {
	d := definition{DSL: &pipeline.DSL{Inputs: []pipeline.InputSpec{{Name: "first", Type: "string"}, {Name: "second", Type: "string"}}}}
	for _, supplied := range []map[string]any{{"undeclared": "private"}, {"first": strings.Repeat("x", 9000), "second": strings.Repeat("y", 9000)}, {"first": strings.Repeat("\n", 9000)}} {
		if got, err := normalizeInputs(d, supplied); got != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("oversized or undeclared input accepted: err=%v", err)
		}
	}
	d.DSL.Inputs = []pipeline.InputSpec{{Name: "value", Type: "object"}}
	if _, err := normalizeInputs(d, map[string]any{"value": map[string]any{}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported input: %v", err)
	}
}

func TestWorkflowTypedDeclarationRejectsUnboundedOrOpaqueInputs(t *testing.T) {
	const simple = `{"dsl_version":"1.0","name":"bounded","inputs":[{"name":"task","type":"string"}],"steps":[{"id":"run","type":"agent_run","agent_slug":"worker","prompt":"{{ inputs.task }}"}]}`
	for _, tc := range []struct{ name, raw string }{
		{"raw size", strings.Repeat(" ", 128<<10) + simple},
		{"invalid JSON", "{"},
		{"unknown root", strings.Replace(simple, `"name":"bounded"`, `"name":"bounded","opaque":true`, 1)},
		{"input format", strings.Replace(simple, `"type":"string"`, `"type":"string","format":"absolute_path"`, 1)},
		{"structured input", strings.Replace(simple, `"type":"string"`, `"type":"object"`, 1)},
		{"oversized prompt", strings.Replace(simple, `{{ inputs.task }}`, strings.Repeat("x", 32769), 1)},
		{"unknown input", strings.Replace(simple, `inputs.task`, `inputs.unknown`, 1)},
		{"future output", strings.Replace(simple, `inputs.task`, `steps.later.output`, 1)},
		{"timeout limit", strings.Replace(simple, `"id":"run"`, `"id":"run","timeout_seconds":301`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := compileTyped(tc.raw, true); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported declaration accepted: %v", err)
			}
		})
	}
	if d, err := compileTyped(simple, true); err != nil || d.AgentSlug != "worker" {
		t.Fatalf("bounded declaration refused: %v", err)
	}
}

func TestWorkflowNestedInputTypesAndReferences(t *testing.T) {
	const nested = `{"dsl_version":"1.0","name":"parent","inputs":[{"name":"task","type":"string"}],"steps":[{"id":"child","type":"call_pipeline","pipeline_slug":"child","inputs":{"value":VALUE}}]}`
	for _, tc := range []struct {
		name, value string
		allowed     bool
	}{
		{"integer", "3", true}, {"boolean", "true", true}, {"string", `"{{ inputs.task }}"`, true},
		{"object", `{"private":"value"}`, false}, {"array", `["value"]`, false}, {"null", `null`, false},
		{"oversized string", `"` + strings.Repeat("x", 16385) + `"`, false},
		{"undeclared reference", `"{{ inputs.unknown }}"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compileTyped(strings.Replace(nested, "VALUE", tc.value, 1), true)
			if (err == nil) != tc.allowed {
				t.Fatalf("typed nested input allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
	if _, err := compileTyped(strings.Replace(nested, "VALUE", "3", 1), false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("graphless nested invocation allowed: %v", err)
	}
	const twoAgents = `{"dsl_version":"1.0","name":"two","steps":[{"id":"a","type":"agent_run","agent_slug":"first","prompt":"hello"},{"id":"b","type":"agent_run","agent_slug":"second","prompt":"hello"}]}`
	if _, err := compileTyped(twoAgents, false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("untyped multi-agent invocation allowed: %v", err)
	}
	for _, value := range []any{false, float64(3), "{{ inputs.count }}", "prefix {{ inputs.count }}", "{{ steps.first.output }}"} {
		got := renderInput(value, map[string]any{"count": float64(7)}, map[string]string{"first": "answer"})
		want := value
		switch value {
		case "{{ inputs.count }}":
			want = float64(7)
		case "prefix {{ inputs.count }}":
			want = "prefix 7"
		case "{{ steps.first.output }}":
			want = "answer"
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rendered typed input %#v: got %#v want %#v", value, got, want)
		}
	}
}
