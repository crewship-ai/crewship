package pipeline

import (
	"math"
	"strings"
	"testing"
)

func TestFormAuthoringRejectsInconsistentFieldContracts(t *testing.T) {
	lo, hi, fraction := 5.0, 2.0, 1.5
	for _, tc := range []struct {
		name string
		spec InputSpec
		part string
	}{
		{"unknown format", InputSpec{Type: "string", Format: "url"}, "format"},
		{"numeric widget on text", InputSpec{Type: "string", Widget: "number"}, "numeric"},
		{"textarea boolean", InputSpec{Type: "boolean", Widget: "textarea"}, "textarea"},
		{"unknown widget", InputSpec{Type: "string", Widget: "slider"}, "unknown widget"},
		{"too many choices", InputSpec{Type: "string", Options: make([]string, 201)}, "200"},
		{"boolean choices", InputSpec{Type: "boolean", Options: []string{"true"}}, "choices require"},
		{"text bounds", InputSpec{Type: "string", Min: &lo}, "numeric field"},
		{"fractional integer bound", InputSpec{Type: "integer", Min: &fraction}, "whole-number"},
		{"reversed bounds", InputSpec{Type: "number", Min: &lo, Max: &hi}, "minimum exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := foreachBoundaryDSL()
			tc.spec.Name = "answer"
			d.Inputs = []InputSpec{tc.spec}
			if err := Validate(d, nil, nil); err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("invalid field accepted or wrong reason: %v", err)
			}
		})
	}
}

func TestFormDispatchRejectsMalformedAnswers(t *testing.T) {
	minimum := 2.0
	for _, tc := range []struct {
		name  string
		spec  InputSpec
		value any
		part  string
	}{
		{"text type", InputSpec{Type: "string", Widget: "text"}, 7, "expected text"},
		{"required blank", InputSpec{Type: "string", Widget: "text", Required: true}, " \t", "answer is required"},
		{"nonfinite number", InputSpec{Type: "number", Widget: "number"}, math.Inf(1), "expected a number"},
		{"below minimum", InputSpec{Type: "number", Min: &minimum}, 1, "below minimum"},
		{"unserializable list", InputSpec{Type: "array", Widget: "textarea"}, []any{make(chan int)}, "expected a list"},
		{"object as list", InputSpec{Type: "array", Widget: "textarea"}, map[string]any{}, "expected a list"},
		{"list as object", InputSpec{Type: "object", Widget: "textarea"}, []any{}, "expected an object"},
		{"unsupported wire type", InputSpec{Type: "unknown", Widget: "text"}, "answer", "unsupported value type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Name = "answer"
			d := &DSL{Inputs: []InputSpec{tc.spec}}
			if err := ValidateFormInputs(d, map[string]any{"answer": tc.value}); err == nil || !strings.Contains(err.Error(), tc.part) {
				t.Fatalf("malformed answer accepted or wrong reason: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		typ   string
		value any
	}{{"string", "literal {{ value }}"}, {"array", []any{"one"}}, {"object", map[string]any{"key": "value"}}} {
		d := &DSL{Inputs: []InputSpec{{Name: "answer", Type: tc.typ, Widget: "textarea"}}}
		if err := ValidateFormInputs(d, map[string]any{"answer": tc.value}); err != nil {
			t.Fatal(err)
		}
	}
}
