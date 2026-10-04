package askforms

import (
	"strings"
	"testing"
)

func TestAnswerValidationOptionalAndSelectionBoundaries(t *testing.T) {
	min, max := 2.0, 1.0
	for _, tc := range []struct {
		name  string
		field Field
		value any
		code  string
	}{
		{"optional attachment", Field{Type: "file"}, nil, ""},
		{"required selection", Field{Type: "multiselect", Required: true}, nil, CodeRequired},
		{"optional selection", Field{Type: "multiselect"}, nil, ""},
		{"unknown selection", Field{Type: "multiselect", Options: []string{"a"}}, []string{"b"}, CodeOptions},
		{"too few options", Field{Type: "multiselect", Min: &min}, []string{"a"}, CodeMin},
		{"too many options", Field{Type: "multiselect", Max: &max}, []string{"a", "b"}, CodeMax},
		{"required consent", Field{Type: "checkbox", Required: true}, false, CodeRequired},
		{"string consent", Field{Type: "checkbox", Required: true}, "yes", ""},
		{"numeric value is not consent", Field{Type: "checkbox", Required: true}, 1, CodeRequired},
		{"required select", Field{Type: "select", Required: true}, "  ", CodeRequired},
		{"optional select", Field{Type: "select"}, "", ""},
		{"optional number", Field{Type: "number"}, "", ""},
		{"required money", Field{Type: "money", Required: true}, "", CodeRequired},
		{"invalid number", Field{Type: "number"}, "twelve", CodeNumber},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := tc.field
			field.Name, field.Label = "answer", "Your answer"
			errs := ValidateAnswers(Form{Fields: []Field{field}}, Values{"answer": tc.value})
			if tc.code == "" {
				if len(errs) != 0 {
					t.Fatalf("optional or valid answer refused: %v", errs)
				}
				return
			}
			if len(errs) != 1 || errs[0].Code != tc.code || errs[0].Field != "answer" || !strings.Contains(errs[0].Error(), "Your answer") {
				t.Fatalf("errors = %v; want one field-specific %q error", errs, tc.code)
			}
		})
	}
}
