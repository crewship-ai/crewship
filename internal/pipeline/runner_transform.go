package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// runTransformStep applies a small jq-flavored expression to the
// rendered Input string. Pure-Go, deterministic, no LLM, no network.
//
// MVP grammar (subset of jq):
//
//	.field         — top-level object field
//	.field.nested  — nested fields (including array indices)
//	.[index]       — array index by integer
//	.field[index]  — combined
//	.              — identity (returns Input as-is)
//	@json          — canonical JSON: parse the (possibly fenced) input
//	                 and re-serialise compact with alphabetically-sorted
//	                 object keys. Makes an upstream agent's JSON output
//	                 byte-stable across runs/tiers regardless of the
//	                 model's whitespace and key-order choices — the
//	                 building block for reproducible "recipe" routines.
//	tostring       — coerce to string (default behaviour for
//	                 primitive results; documented for clarity)
//	length         — array/string/object length
//	keys           — object keys as array (sorted)
//
// Anything outside this set is a parse error. We deliberately keep
// the grammar small: transform steps are for plumbing, not
// computation. If you need real logic, use a code step.
func (e *Executor) runTransformStep(step Step, parentRender RenderContext) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.Transform == nil {
		return "", 0, 0, fmt.Errorf("transform step missing body")
	}

	rawInput := Render(step.Transform.Input, parentRender)
	expr := strings.TrimSpace(step.Transform.Expression)

	// Identity short-circuit.
	if expr == "." || expr == "" {
		return rawInput, 0, time.Since(stepStart).Milliseconds(), nil
	}

	// Parse JSON. DecodeAgentJSON strips the fence/preamble noise
	// that LLM outputs commonly carry, so a transform step can read
	// upstream agent output without each pipeline having to add a
	// "strip the fence first" prelude. Non-JSON input still falls
	// through to the identity short-circuit for "tostring"/"length"
	// on raw strings.
	var v any
	// UseNumber-decode so numeric tokens keep full precision through @json
	// canonicalisation (and through path/tostring output) instead of being
	// flattened to float64.
	if err := DecodeAgentJSONNumber(rawInput, &v); err != nil {
		switch expr {
		case "length":
			return fmt.Sprintf("%d", len(rawInput)), 0, time.Since(stepStart).Milliseconds(), nil
		case "tostring":
			return rawInput, 0, time.Since(stepStart).Milliseconds(), nil
		case "@json", "tojson":
			// Non-JSON input + @json: encode the raw input AS a JSON string
			// literal (quotes added, newlines/quotes/control chars escaped).
			// This is the safe way to drop agent plain-text into a JSON body —
			// e.g. an http step body `{"content": {{ steps.x | @json }}}` — without
			// relying on the agent to emit perfectly-escaped JSON itself.
			b, err := json.Marshal(rawInput)
			if err != nil {
				return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("@json: marshal raw string: %w", err)
			}
			return string(b), 0, time.Since(stepStart).Milliseconds(), nil
		default:
			return "", 0, time.Since(stepStart).Milliseconds(),
				fmt.Errorf("transform step %q input is not JSON and expression %q requires JSON", step.ID, expr)
		}
	}

	out, err := evalTransform(v, expr)
	if err != nil {
		return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("transform step %q: %w", step.ID, err)
	}
	return out, 0, time.Since(stepStart).Milliseconds(), nil
}

// evalTransform applies the expression to the parsed JSON value.
// Returns the stringified result.
func evalTransform(v any, expr string) (string, error) {
	switch expr {
	case "length":
		switch x := v.(type) {
		case []any:
			return fmt.Sprintf("%d", len(x)), nil
		case map[string]any:
			return fmt.Sprintf("%d", len(x)), nil
		case string:
			return fmt.Sprintf("%d", len(x)), nil
		default:
			return "", fmt.Errorf("length: not an array/object/string")
		}
	case "keys":
		m, ok := v.(map[string]any)
		if !ok {
			return "", fmt.Errorf("keys: not an object")
		}
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		// Sort for determinism — jq keys sorts alphabetically too.
		sort.Strings(out)
		b, _ := json.Marshal(out)
		return string(b), nil
	case "tostring":
		return stringify(v), nil
	case "@json", "tojson":
		// Canonical re-serialisation: encoding/json emits compact output
		// and sorts map keys alphabetically, so two semantically-equal
		// inputs that differ only in whitespace or key order collapse to
		// the identical byte string. Array element order is preserved.
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("@json: marshal: %w", err)
		}
		return string(b), nil
	}

	parts, err := parseTransformPath(expr)
	if err != nil {
		return "", err
	}
	cursor := v
	for _, part := range parts {
		if part.array {
			arr, ok := cursor.([]any)
			if !ok {
				return "", fmt.Errorf("cannot index non-array")
			}
			if part.index >= len(arr) {
				return "", fmt.Errorf("index %d out of range (len=%d)", part.index, len(arr))
			}
			cursor = arr[part.index]
			continue
		}
		obj, ok := cursor.(map[string]any)
		if !ok {
			return "", fmt.Errorf("cannot access field %q on non-object", part.field)
		}
		next, ok := obj[part.field]
		if !ok {
			return "", fmt.Errorf("field %q not found", part.field)
		}
		cursor = next
	}
	return stringify(cursor), nil
}

type transformPathPart struct {
	field string
	index int
	array bool
}

// Validate syntax independently of runtime JSON. The evaluator uses the same
// parser so admission cannot promise expressions the runtime does not support.
func validateTransformExpression(expression string) error {
	expression = strings.TrimSpace(expression)
	switch expression {
	case ".", "length", "keys", "tostring", "@json", "tojson":
		return nil
	}
	_, err := parseTransformPath(expression)
	return err
}

func parseTransformPath(expression string) ([]transformPathPart, error) {
	if !strings.HasPrefix(expression, ".") {
		return nil, fmt.Errorf("expression %q must start with '.' or be one of: length, keys, tostring, @json (alias tojson)", expression)
	}
	rest := expression[1:]
	var parts []transformPathPart
	for rest != "" {
		if strings.HasPrefix(rest, "[") {
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("unclosed [ in expression")
			}
			index, err := strconv.Atoi(strings.TrimSpace(rest[1:end]))
			if err != nil || index < 0 {
				return nil, fmt.Errorf("array index %q must be a non-negative integer", rest[1:end])
			}
			parts = append(parts, transformPathPart{array: true, index: index})
			rest = rest[end+1:]
		} else {
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			field := rest[:end]
			if field == "" || field != strings.TrimSpace(field) || strings.ContainsAny(field, "]|*+/><=(){} ,\t\n\r") {
				return nil, fmt.Errorf("invalid field %q in expression; use code for computation", field)
			}
			parts = append(parts, transformPathPart{field: field})
			rest = rest[end:]
		}
		if strings.HasPrefix(rest, ".") {
			rest = rest[1:]
			if rest == "" {
				return nil, fmt.Errorf("expression ends with an empty field")
			}
		} else if rest != "" && !strings.HasPrefix(rest, "[") {
			return nil, fmt.Errorf("expected '.' or '[' in expression at %q", rest)
		}
	}
	return parts, nil
}
