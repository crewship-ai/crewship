package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

var inputFormNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// Existing unannotated inputs retain their historical contract. Declaring a
// form opts into validation at authoring and execution, across every producer.
func hasInputForm(in InputSpec) bool { return in.Widget != "" || len(in.Options) > 0 || in.AllowCustom }

func validateInputForms(dsl *DSL) error {
	seen := map[string]int{}
	for _, in := range dsl.Inputs {
		seen[in.Name]++
	}
	for _, in := range dsl.Inputs {
		if !hasInputForm(in) {
			continue
		}
		if !inputFormNameRE.MatchString(in.Name) || seen[in.Name] > 1 {
			return fmt.Errorf("input %q needs a unique variable name", in.Name)
		}
		valid := map[string]string{"text": "string", "select": "string", "multiselect": "array", "boolean": "boolean"}
		if t, ok := valid[in.Widget]; ok && t != in.Type {
			return fmt.Errorf("input %q: %s requires type %s", in.Name, in.Widget, t)
		}
		switch in.Widget {
		case "", "text", "select", "multiselect", "boolean":
		case "number":
			if in.Type != "number" && in.Type != "integer" {
				return fmt.Errorf("input %q: number requires a numeric type", in.Name)
			}
		case "textarea":
			if in.Type != "string" && in.Type != "array" && in.Type != "object" {
				return fmt.Errorf("input %q: invalid textarea type", in.Name)
			}
		default:
			return fmt.Errorf("input %q: unknown widget", in.Name)
		}
		if (in.Widget == "select" || in.Widget == "multiselect") && len(in.Options) == 0 {
			return fmt.Errorf("input %q: add at least one choice", in.Name)
		}
		if len(in.Options) > 200 {
			return fmt.Errorf("input %q: at most 200 choices", in.Name)
		}
		if len(in.Options) > 0 && in.Type != "string" && in.Type != "array" {
			return fmt.Errorf("input %q: choices require string or array", in.Name)
		}
		if in.AllowCustom && (in.Type != "string" || len(in.Options) == 0) {
			return fmt.Errorf("input %q: custom answers require a single-choice string field", in.Name)
		}
		options := map[string]bool{}
		for _, option := range in.Options {
			if strings.TrimSpace(option) == "" || options[option] {
				return fmt.Errorf("input %q: choices must be nonempty and unique", in.Name)
			}
			options[option] = true
		}
		if (in.Min != nil || in.Max != nil) && in.Type != "number" && in.Type != "integer" {
			return fmt.Errorf("input %q: bounds require a numeric field", in.Name)
		}
		if in.Type == "integer" && ((in.Min != nil && math.Trunc(*in.Min) != *in.Min) || (in.Max != nil && math.Trunc(*in.Max) != *in.Max)) {
			return fmt.Errorf("input %q: whole-number bounds required", in.Name)
		}
		if in.Min != nil && in.Max != nil && *in.Min > *in.Max {
			return fmt.Errorf("input %q: minimum exceeds maximum", in.Name)
		}
		if in.Default != nil {
			if err := validateFormValue(in, in.Default); err != nil {
				return fmt.Errorf("default %w", err)
			}
		}
	}
	return nil
}

// ValidateFormInputs checks declared forms before dispatch, using exactly the
// defaults the renderer uses. Answer strings remain data, never template code.
func ValidateFormInputs(dsl *DSL, supplied map[string]any) error {
	values := mergeInputs(supplied, dsl)
	for _, in := range dsl.Inputs {
		if !hasInputForm(in) {
			continue
		}
		value, exists := values[in.Name]
		if !exists || value == nil {
			if in.Required {
				return fmt.Errorf("input %q is required", in.Name)
			}
			continue
		}
		if err := validateFormValue(in, value); err != nil {
			return err
		}
	}
	return nil
}

func validateFormValue(in InputSpec, value any) error {
	fail := func(reason string) error { return fmt.Errorf("input %q: %s", in.Name, reason) }
	choice := func(s string) bool {
		for _, opt := range in.Options {
			if s == opt {
				return true
			}
		}
		return false
	}
	switch in.Type {
	case "string":
		s, ok := value.(string)
		if !ok {
			return fail("expected text")
		}
		if in.Required && strings.TrimSpace(s) == "" {
			return fail("an answer is required")
		}
		if len(in.Options) > 0 && !in.AllowCustom && !choice(s) {
			return fail("choose one of the available answers")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("expected true or false")
		}
	case "integer", "number":
		b, err := json.Marshal(value)
		if err != nil {
			return fail("expected a number")
		}
		var n float64
		if json.Unmarshal(b, &n) != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fail("expected a number")
		}
		if in.Type == "integer" && math.Trunc(n) != n {
			return fail("expected a whole number")
		}
		if in.Min != nil && n < *in.Min {
			return fail("below minimum")
		}
		if in.Max != nil && n > *in.Max {
			return fail("above maximum")
		}
	case "array":
		b, err := json.Marshal(value)
		if err != nil {
			return fail("expected a list")
		}
		var values []any
		if json.Unmarshal(b, &values) != nil || values == nil {
			return fail("expected a list")
		}
		if in.Required && len(values) == 0 {
			return fail("choose at least one answer")
		}
		if len(in.Options) > 0 {
			seen := map[string]bool{}
			for _, v := range values {
				s, ok := v.(string)
				if !ok || !choice(s) || seen[s] {
					return fail("choose distinct available answers")
				}
				seen[s] = true
			}
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fail("expected an object")
		}
	default:
		return fail("unsupported value type")
	}
	return nil
}
