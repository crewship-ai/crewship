package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestTransformExpression_RejectedAtAdmission(t *testing.T) {
	for _, expression := range []string{`{total: (.qty * 2)}`, `.a + .b`, `.qty+1`, `.qty > 2`, `.a | length`, `.qty * .unit`, `.items[0foo]`, `.items[`, `.items[]`, `.items[0]tail`, `.a..b`, `.a.`} {
		t.Run(expression, func(t *testing.T) {
			dsl := DSL{Name: "invalid-transform", Agentless: true, Steps: []Step{{ID: "extract", Type: StepTransform, Transform: &TransformStep{Input: `{"qty":2}`, Expression: expression}}}}
			if err := Validate(&dsl, nil, nil); err == nil {
				t.Fatal("invalid transform expression passed admission")
			} else if !strings.Contains(err.Error(), "extract") || !strings.Contains(err.Error(), "expression") {
				t.Fatalf("error does not identify the step/expression: %v", err)
			}
		})
	}
}

func TestTransformExpression_AdmissionDoesNotRequireRuntimeData(t *testing.T) {
	for _, expression := range []string{`.`, `.customer.name`, `.items[0].price`, `.[1][0]`, `.snake_case.dashed-key`, `length`, `keys`, `tostring`, `@json`, `tojson`} {
		t.Run(expression, func(t *testing.T) {
			dsl := &DSL{Name: "valid-transform", Agentless: true, Inputs: []InputSpec{{Name: "payload", Type: "object"}}, Steps: []Step{{ID: "extract", Type: StepTransform, Transform: &TransformStep{Input: "{{ inputs.payload }}", Expression: expression}}}}
			if err := Validate(dsl, nil, nil); err != nil {
				t.Fatalf("valid expression rejected before inputs exist: %v", err)
			}
		})
	}
}

func TestTransformExpression_PreservesWhitespaceInArrayIndex(t *testing.T) {
	for _, expression := range []string{".[ 0 ]", ".items[ 0 ].value"} {
		dsl := &DSL{Name: "spaced-index", Agentless: true, Steps: []Step{{ID: "extract", Type: StepTransform, Transform: &TransformStep{Input: `{"items":[{"value":7}]}`, Expression: expression}}}}
		if err := Validate(dsl, nil, nil); err != nil {
			t.Fatalf("legacy index rejected: %v", err)
		}
		var value any = []any{7}
		if strings.HasPrefix(expression, ".items") {
			value = map[string]any{"items": []any{map[string]any{"value": 7}}}
		}
		output, err := evalTransform(value, expression)
		if err != nil || output != "7" {
			t.Fatalf("legacy index output=%q err=%v", output, err)
		}
	}
}

func TestTransformExpression_LegacySkippedStepDoesNotAbortRun(t *testing.T) {
	definition := `{"name":"legacy-skipped","agentless":true,"steps":[{"id":"unused","type":"transform","if":"false","transform":{"input":"{}","expression":".a + .b"}},{"id":"result","type":"transform","transform":{"input":"ok","expression":"."}}]}`
	dsl, err := Parse([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("authoring must reject unsupported syntax, including skipped branches")
	}
	got := runToTerminal(t, "legacy-skipped", definition, newMockRunner(), context.Background())
	if got.rec.Status != RunStatusCompleted || got.rec.Output != "ok" {
		t.Fatalf("stored skipped branch blocked execution: %+v", got.rec)
	}
}
