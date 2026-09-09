package pipeline

import (
	"testing"
)

func TestInputFormChoices(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  InputSpec
		value any
		pass  bool
	}{
		{"choice", InputSpec{Name: "answer", Type: "string", Widget: "select", Options: []string{"a", "b"}}, "a", true},
		{"reject other", InputSpec{Name: "answer", Type: "string", Widget: "select", Options: []string{"a", "b"}}, "c", false},
		{"custom", InputSpec{Name: "answer", Type: "string", Widget: "select", Options: []string{"a"}, AllowCustom: true}, "custom {{ literal }}", true},
		{"multi", InputSpec{Name: "answer", Type: "array", Widget: "multiselect", Options: []string{"a,b", "c"}}, []any{"a,b", "c"}, true},
		{"multi invalid", InputSpec{Name: "answer", Type: "array", Widget: "multiselect", Options: []string{"a"}}, []any{"b"}, false},
		{"duplicate", InputSpec{Name: "answer", Type: "array", Widget: "multiselect", Options: []string{"a"}}, []any{"a", "a"}, false},
		{"required false", InputSpec{Name: "answer", Type: "boolean", Widget: "boolean", Required: true}, false, true},
		{"required empty", InputSpec{Name: "answer", Type: "array", Widget: "multiselect", Required: true, Options: []string{"a"}}, []any{}, false},
		{"typed number", InputSpec{Name: "answer", Type: "integer", Widget: "number"}, "12", false},
		{"integer", InputSpec{Name: "answer", Type: "integer", Widget: "number"}, float64(12), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsl := &DSL{Inputs: []InputSpec{tc.spec}}
			err := ValidateFormInputs(dsl, map[string]any{"answer": tc.value})
			if (err == nil) != tc.pass {
				t.Fatalf("err=%v, pass=%v", err, tc.pass)
			}
		})
	}
}

func TestInputFormDefaultsAndLegacy(t *testing.T) {
	dsl := &DSL{Inputs: []InputSpec{{Name: "choice", Type: "string", Widget: "select", Options: []string{"a"}, Default: "a", Required: true}}}
	if err := ValidateFormInputs(dsl, nil); err != nil {
		t.Fatal(err)
	}
	dsl.Inputs[0].Default = "bad"
	if err := validateInputForms(dsl); err == nil {
		t.Fatal("invalid default accepted")
	}
	dsl.Inputs[0].Default = nil
	if err := ValidateFormInputs(dsl, nil); err == nil {
		t.Fatal("missing required accepted")
	}
	legacy := &DSL{Inputs: []InputSpec{{Name: "old", Type: "integer", Required: true}}}
	if err := ValidateFormInputs(legacy, nil); err != nil {
		t.Fatalf("changed legacy contract: %v", err)
	}
}

func TestInputFormMalformedDefinition(t *testing.T) {
	for _, spec := range []InputSpec{
		{Name: "a", Type: "string", Widget: "select"},
		{Name: "a", Type: "string", Widget: "select", Options: []string{"a", "a"}},
		{Name: "a", Type: "string", Widget: "select", Options: []string{""}},
		{Name: "a", Type: "integer", Widget: "select", Options: []string{"a"}},
		{Name: "a", Type: "array", Widget: "multiselect", Options: []string{"a"}, AllowCustom: true},
		{Name: "bad name", Type: "string", Widget: "text"},
	} {
		if err := validateInputForms(&DSL{Inputs: []InputSpec{spec}}); err == nil {
			t.Fatalf("accepted %+v", spec)
		}
	}
}
