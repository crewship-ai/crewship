package lookout

import (
	"errors"
	"fmt"
	"strings"
)

// Schema is a minimal JSON-Schema-shaped descriptor used by ValidateArgs.
// It is a struct rather than a map so callers get compile-time field
// checking; the field set covers the subset we actually need to validate
// LLM tool calls (type, properties, required, items, enum). For richer
// schemas the package can later swap in github.com/santhosh-tekuri/jsonschema
// without changing the call site — ValidateArgs is the only public surface.
type Schema struct {
	// Type is one of "string", "number", "integer", "boolean", "array",
	// "object", "null". Empty type means "any".
	Type string `json:"type,omitempty"`
	// Properties maps property name -> nested schema. Only meaningful for
	// Type=="object".
	Properties map[string]Schema `json:"properties,omitempty"`
	// Required lists property names that must be present and non-null on
	// objects. Order is irrelevant.
	Required []string `json:"required,omitempty"`
	// Items describes the schema each element of an array must satisfy.
	// Only meaningful for Type=="array".
	Items *Schema `json:"items,omitempty"`
	// Enum, when non-empty, restricts the value to one of the listed
	// constants. Comparison is via Go's == on the JSON-decoded value.
	Enum []any `json:"enum,omitempty"`
	// AdditionalProperties, when set to false, rejects unknown keys on
	// object inputs. nil means "permissive" (default JSON-Schema behaviour).
	AdditionalProperties *bool `json:"additionalProperties,omitempty"`
}

// ArgsInvalidError is returned by ValidateArgs when the input does not
// conform to the schema. Middleware unwraps it via errors.As to drive a
// retry-with-correction loop against the LLM. Path is a dotted JSON path
// (e.g. "config.timeout") that pinpoints the offending field.
type ArgsInvalidError struct {
	Path   string
	Reason string
}

func (e *ArgsInvalidError) Error() string {
	if e.Path == "" {
		return "lookout: args invalid: " + e.Reason
	}
	return fmt.Sprintf("lookout: args invalid at %s: %s", e.Path, e.Reason)
}

// ValidateArgs checks that args conform to schema. Returns nil on success
// or an *ArgsInvalidError describing the first problem encountered. The
// validation is intentionally shallow but structurally complete: it walks
// nested objects/arrays following the schema.
func ValidateArgs(schema Schema, args map[string]any) error {
	return validateValue("", schema, args)
}

func validateValue(path string, schema Schema, value any) error {
	if value == nil {
		// JSON null. Allowed unless the schema forbids it via type.
		if schema.Type != "" && schema.Type != "null" {
			return &ArgsInvalidError{Path: path, Reason: "value is null"}
		}
		return nil
	}
	if schema.Type != "" {
		if err := checkType(path, schema.Type, value); err != nil {
			return err
		}
	}
	if len(schema.Enum) > 0 {
		matched := false
		for _, want := range schema.Enum {
			if want == value {
				matched = true
				break
			}
		}
		if !matched {
			return &ArgsInvalidError{Path: path, Reason: "value not in enum"}
		}
	}
	switch schema.Type {
	case "object":
		obj, ok := value.(map[string]any)
		if !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected object"}
		}
		for _, key := range schema.Required {
			v, present := obj[key]
			if !present || v == nil {
				return &ArgsInvalidError{Path: joinPath(path, key), Reason: "required field missing"}
			}
		}
		for key, sub := range schema.Properties {
			if v, ok := obj[key]; ok {
				if err := validateValue(joinPath(path, key), sub, v); err != nil {
					return err
				}
			}
		}
		if schema.AdditionalProperties != nil && !*schema.AdditionalProperties {
			for key := range obj {
				if _, declared := schema.Properties[key]; !declared {
					return &ArgsInvalidError{Path: joinPath(path, key), Reason: "additional property not allowed"}
				}
			}
		}
	case "array":
		arr, ok := value.([]any)
		if !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected array"}
		}
		if schema.Items != nil {
			for i, item := range arr {
				if err := validateValue(fmt.Sprintf("%s[%d]", path, i), *schema.Items, item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkType verifies value matches the JSON Schema primitive type tag.
// Numeric handling is deliberately lenient: JSON has only one number type
// but Go decoders may surface int, int64, float64, etc.
func checkType(path, want string, value any) error {
	switch want {
	case "string":
		if _, ok := value.(string); !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected string"}
		}
	case "number":
		if !isNumber(value) {
			return &ArgsInvalidError{Path: path, Reason: "expected number"}
		}
	case "integer":
		if !isInteger(value) {
			return &ArgsInvalidError{Path: path, Reason: "expected integer"}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected boolean"}
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected array"}
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return &ArgsInvalidError{Path: path, Reason: "expected object"}
		}
	case "null":
		if value != nil {
			return &ArgsInvalidError{Path: path, Reason: "expected null"}
		}
	default:
		return &ArgsInvalidError{Path: path, Reason: "unknown type: " + want}
	}
	return nil
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	}
	return false
}

func isInteger(v any) bool {
	switch n := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return float32(int64(n)) == n
	case float64:
		return float64(int64(n)) == n
	}
	return false
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

// IsArgsInvalid reports whether err is or wraps an *ArgsInvalidError. Useful
// for middleware that needs to branch on retryable vs fatal validation
// failures without importing the concrete type.
func IsArgsInvalid(err error) bool {
	var target *ArgsInvalidError
	return errors.As(err, &target)
}

// describeForRetry converts an *ArgsInvalidError into a short human
// sentence suitable to feed back to the LLM as a correction hint. It
// deliberately omits the offending value to avoid echoing user-controlled
// content into the next prompt.
func describeForRetry(err error) string {
	var v *ArgsInvalidError
	if !errors.As(err, &v) {
		return strings.TrimSpace(err.Error())
	}
	if v.Path == "" {
		return v.Reason
	}
	return fmt.Sprintf("field %q: %s", v.Path, v.Reason)
}
