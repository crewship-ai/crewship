package main

import "testing"

func TestCrewBudgetRequestUsesOAS30NumericBounds(t *testing.T) {
	schema := compileOAS30At(t, specBytes(t), "/components/schemas/CrewBudgetRequestV1")
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"small positive", `{"limit_usd":0.001}`, true},
		{"upper inclusive", `{"limit_usd":1000000000}`, true},
		{"zero", `{"limit_usd":0}`, false},
		{"negative", `{"limit_usd":-1}`, false},
		{"above upper", `{"limit_usd":1000000000.01}`, false},
		{"missing", `{}`, false},
		{"string", `{"limit_usd":"1"}`, false},
		{"null", `{"limit_usd":null}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkNullableData(t, schema, tc.body, tc.valid) })
	}
}
