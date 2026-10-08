package pipeline

import (
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
