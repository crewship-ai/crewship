package pipeline

import (
	"fmt"
	"strings"
)

// A whole reference bound to a declared structured/scalar input carries data,
// not prompt text. Mixed text and undeclared/string inputs retain Render's
// legacy behavior. Do not recursively reinterpret literal maps or arrays.
func renderNestedInputs(values map[string]any, specs []InputSpec, render RenderContext) (map[string]any, error) {
	byName := make(map[string]InputSpec, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = spec
	}
	out := make(map[string]any, len(values))
	for name, value := range values {
		text, ok := value.(string)
		if !ok {
			out[name] = value
			continue
		}
		out[name] = Render(text, render)
		spec := byName[name]
		switch spec.Type {
		case "integer", "number", "boolean", "array", "object":
		default:
			continue
		}
		trimmed := strings.TrimSpace(text)
		match := templateRE.FindStringSubmatch(trimmed)
		if len(match) != 2 || match[0] != trimmed {
			continue
		}
		ref := strings.TrimSpace(match[1])
		// Do not widen the set of namespaces that can supply structured data.
		if !strings.HasPrefix(ref, "inputs.") && !strings.HasPrefix(ref, "steps.") {
			continue
		}
		if !referenceValueExists(ref, render) {
			if !spec.Required {
				delete(out, name)
				continue
			}
			return nil, fmt.Errorf("input %q: referenced value is unavailable", name)
		}
		var decoded any
		parts := strings.SplitN(ref, ".", 3)
		_, fenced := render.UntrustedInputs[parts[1]]
		if parts[0] == "inputs" && fenced {
			return nil, fmt.Errorf("input %q: fenced prompt data cannot be bound as %s", name, spec.Type)
		}
		if parts[0] == "inputs" {
			decoded = render.Inputs[parts[1]]
			if len(parts) == 3 {
				decoded = decoded.(map[string]any)[parts[2]]
			}
		} else if parts[0] == "steps" && strings.HasPrefix(parts[2], "output.") {
			decoded, _ = jsonPathValueFound(render.StepOutputs[parts[1]], strings.TrimPrefix(parts[2], "output."))
		} else if err := DecodeAgentJSON(out[name].(string), &decoded); err != nil {
			return nil, fmt.Errorf("input %q: referenced value must be valid JSON of type %s", name, spec.Type)
		}
		if decoded == nil {
			if spec.Required {
				return nil, fmt.Errorf("input %q is required", name)
			}
		} else if err := validateFormValue(spec, decoded); err != nil {
			return nil, err
		}
		out[name] = decoded
	}
	return out, nil
}
