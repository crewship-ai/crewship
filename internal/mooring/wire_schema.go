package mooring

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrWireSchema = errors.New("unsupported or invalid Mooring wire schema")
var ErrWireShape = errors.New("invalid Mooring wire envelope")

// WireSchema is an unused interpreter for the frozen Mooring schema vocabulary,
// not a general JSON Schema implementation. Construction audits ALL branches,
// including those under not, before any message may be validated. A caller
// provides a trusted public schema snapshot; messages never select their schema.
// Shape validity conveys no authority, direction, scope equality or app semantics.
type WireSchema struct {
	root           map[string]any
	patterns       map[string]*regexp.Regexp
	maxSchemaDepth int
}

// NewWireSchema fails closed on unsupported constraints or patterns. There is no
// remote reference resolution, executable schema code or private build dependency.
func NewWireSchema(raw []byte) (*WireSchema, error) {
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return nil, ErrWireSchema
	}
	s := &WireSchema{patterns: map[string]*regexp.Regexp{}}
	if json.Unmarshal(canonical, &s.root) != nil || s.root == nil {
		return nil, ErrWireSchema
	}
	if err := s.audit(s.root, 0); err != nil {
		return nil, err
	}
	depth, err := s.expandedDepth(s.root, map[string]bool{}, map[string]int{})
	if err != nil {
		return nil, err
	}
	s.maxSchemaDepth = depth
	return s, nil
}
func wireObject(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }
func wireArray(v any) ([]any, bool)           { a, ok := v.([]any); return a, ok }
func wholeBound(v any) bool {
	n, ok := v.(float64)
	return ok && n >= 0 && n <= 16777216 && n == float64(int(n))
}
func supportedWirePattern(p string) bool {
	switch p {
	case `^(0|[1-9][0-9]?|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`, "\x00", "[^0-9.]", "[^0-9]", "[^A-Za-z0-9+/=]", `^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`, `^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`, `^sha256:[0-9a-f]{64}$`:
		return true
	}
	// Counter's exact frozen language; this is not an arbitrary-regex escape hatch.
	return p == `^(0|[1-9][0-9]{0,18}|1[0-7][0-9]{18}|18[0-3][0-9]{17}|184[0-3][0-9]{16}|1844[0-5][0-9]{15}|18446[0-6][0-9]{14}|184467[0-3][0-9]{13}|1844674[0-3][0-9]{12}|184467440[0-6][0-9]{10}|1844674407[0-2][0-9]{9}|18446744073[0-6][0-9]{8}|1844674407370[0-8][0-9]{6}|18446744073709[0-4][0-9]{5}|184467440737095[0-4][0-9]{4}|18446744073709550[0-9]{3}|18446744073709551[0-5][0-9]{2}|1844674407370955160[0-9]{1}|1844674407370955161[0-4]|18446744073709551615)$`
}
func (s *WireSchema) resolve(ref string) (map[string]any, bool) {
	if !strings.HasPrefix(ref, "#/$defs/") {
		return nil, false
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	if name == "" || strings.ContainsAny(name, "/~") {
		return nil, false
	}
	defs, ok := wireObject(s.root["$defs"])
	if !ok {
		return nil, false
	}
	return wireObject(defs[name])
}
func (s *WireSchema) audit(n map[string]any, depth int) error {
	if depth > 64 {
		return ErrWireSchema
	}
	for k, v := range n {
		switch k {
		case "$schema":
			if v != "https://json-schema.org/draft/2020-12/schema" {
				return ErrWireSchema
			}
		case "title", "description", "x-schema-version", "x-direction":
			if _, ok := v.(string); !ok {
				return ErrWireSchema
			}
		case "$ref":
			r, ok := v.(string)
			if !ok {
				return ErrWireSchema
			}
			if _, ok = s.resolve(r); !ok {
				return ErrWireSchema
			}
		case "type":
			t, ok := v.(string)
			if !ok {
				return ErrWireSchema
			}
			switch t {
			case "object", "array", "string", "boolean":
			default:
				return ErrWireSchema
			}
		case "properties", "$defs":
			m, ok := wireObject(v)
			if !ok {
				return ErrWireSchema
			}
			for _, child := range m {
				c, ok := wireObject(child)
				if !ok {
					return ErrWireSchema
				}
				if err := s.audit(c, depth+1); err != nil {
					return err
				}
			}
		case "items", "if", "then", "else", "not":
			c, ok := wireObject(v)
			if !ok {
				return ErrWireSchema
			}
			if err := s.audit(c, depth+1); err != nil {
				return err
			}
		case "oneOf", "allOf", "anyOf":
			a, ok := wireArray(v)
			if !ok || len(a) == 0 {
				return ErrWireSchema
			}
			for _, child := range a {
				c, ok := wireObject(child)
				if !ok {
					return ErrWireSchema
				}
				if err := s.audit(c, depth+1); err != nil {
					return err
				}
			}
		case "pattern":
			p, ok := v.(string)
			if !ok || !supportedWirePattern(p) {
				return ErrWireSchema
			}
			r, err := regexp.Compile(p)
			if err != nil {
				return ErrWireSchema
			}
			s.patterns[p] = r
		case "minLength", "maxLength", "minItems", "maxItems":
			if !wholeBound(v) {
				return ErrWireSchema
			}
		case "required":
			a, ok := wireArray(v)
			if !ok {
				return ErrWireSchema
			}
			seen := map[string]bool{}
			for _, x := range a {
				k, ok := x.(string)
				if !ok || seen[k] {
					return ErrWireSchema
				}
				seen[k] = true
			}
		case "enum":
			a, ok := wireArray(v)
			if !ok || len(a) == 0 {
				return ErrWireSchema
			}
		case "const": // JSON-valued const is declarative, never executable.
		case "additionalProperties":
			if _, ok := v.(bool); !ok {
				return ErrWireSchema
			}
		case "uniqueItems":
			if _, ok := v.(bool); !ok {
				return ErrWireSchema
			}
		default:
			return ErrWireSchema
		}
	}
	return nil
}

// ValidateE3 returns canonical bytes only after bounded strict JSON and the
// entire envelope shape pass. Authenticate the channel and validate semantic
// cross-field invariants separately before dispatch; this never issues Saved.
func (s *WireSchema) ValidateE3(raw []byte) ([]byte, error) {
	if s == nil || s.root == nil {
		return nil, ErrWireSchema
	}
	canonical, err := CanonicalE3WireJSON(raw)
	if err != nil {
		return nil, err
	}
	var value any
	if json.Unmarshal(canonical, &value) != nil {
		return nil, ErrWireShape
	}
	if !s.matches(s.root, value, 0) {
		return nil, ErrWireShape
	}
	return canonical, nil
}
func (s *WireSchema) matches(n map[string]any, v any, depth int) bool {
	if depth > s.maxSchemaDepth {
		return false
	}
	if ref, ok := n["$ref"].(string); ok {
		r, ok := s.resolve(ref)
		if !ok || !s.matches(r, v, depth+1) {
			return false
		}
	}
	if t, ok := n["type"].(string); ok {
		valid := false
		switch t {
		case "object":
			_, valid = wireObject(v)
		case "array":
			_, valid = wireArray(v)
		case "string":
			_, valid = v.(string)
		case "boolean":
			_, valid = v.(bool)
		}
		if !valid {
			return false
		}
	}
	if c, ok := n["const"]; ok && !reflect.DeepEqual(c, v) {
		return false
	}
	if a, ok := wireArray(n["enum"]); ok {
		found := false
		for _, x := range a {
			if reflect.DeepEqual(x, v) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if text, ok := v.(string); ok {
		size := utf8.RuneCountInString(text)
		if min, ok := n["minLength"].(float64); ok && size < int(min) {
			return false
		}
		if max, ok := n["maxLength"].(float64); ok && size > int(max) {
			return false
		}
		if p, ok := n["pattern"].(string); ok {
			r := s.patterns[p]
			if r == nil || !r.MatchString(text) {
				return false
			}
		}
	}
	if a, ok := wireArray(v); ok {
		if min, ok := n["minItems"].(float64); ok && len(a) < int(min) {
			return false
		}
		if max, ok := n["maxItems"].(float64); ok && len(a) > int(max) {
			return false
		}
		if unique, _ := n["uniqueItems"].(bool); unique {
			seen := make(map[string]bool, len(a))
			for _, x := range a {
				// Values already passed strict JCS/binary64 normalization.
				// Go's deterministic map encoding provides structural equality
				// keys without a quadratic comparison of wide arrays.
				encoded, err := json.Marshal(x)
				if err != nil || seen[string(encoded)] {
					return false
				}
				seen[string(encoded)] = true
			}
		}
		if item, ok := wireObject(n["items"]); ok {
			for _, x := range a {
				if !s.matches(item, x, depth+1) {
					return false
				}
			}
		}
	}
	if m, ok := wireObject(v); ok {
		if req, ok := wireArray(n["required"]); ok {
			for _, x := range req {
				if _, exists := m[x.(string)]; !exists {
					return false
				}
			}
		}
		props, _ := wireObject(n["properties"])
		for k, x := range m {
			if prop, ok := wireObject(props[k]); ok {
				if !s.matches(prop, x, depth+1) {
					return false
				}
			} else if additional, exists := n["additionalProperties"].(bool); exists && !additional {
				return false
			}
		}
	}
	for _, kind := range []string{"allOf", "anyOf", "oneOf"} {
		if a, ok := wireArray(n[kind]); ok {
			count := 0
			for _, x := range a {
				child, _ := wireObject(x)
				if s.matches(child, v, depth+1) {
					count++
				}
			}
			if kind == "allOf" && count != len(a) || kind == "anyOf" && count == 0 || kind == "oneOf" && count != 1 {
				return false
			}
		}
	}
	if child, ok := wireObject(n["not"]); ok {
		if s.matches(child, v, depth+1) {
			return false
		}
	}
	if cond, ok := wireObject(n["if"]); ok {
		branch := "else"
		if s.matches(cond, v, depth+1) {
			branch = "then"
		}
		if child, ok := wireObject(n[branch]); ok && !s.matches(child, v, depth+1) {
			return false
		}
	}
	return true
}

// expandedDepth proves the trusted schema reference graph acyclic and derives
// the validator recursion bound from that graph, rather than rejecting valid
// messages through an unadvertised node-visit quota. All branches, even unused
// definitions and negations, participate. No external references are followed.
func (s *WireSchema) expandedDepth(n map[string]any, active map[string]bool, memo map[string]int) (int, error) {
	maxDepth := 0
	visit := func(child map[string]any) error {
		d, err := s.expandedDepth(child, active, memo)
		if err != nil {
			return err
		}
		if d > maxDepth {
			maxDepth = d
		}
		return nil
	}
	for k, v := range n {
		switch k {
		case "$ref":
			ref := v.(string)
			if active[ref] {
				return 0, ErrWireSchema
			}
			d, ok := memo[ref]
			if !ok {
				active[ref] = true
				child, _ := s.resolve(ref)
				var err error
				d, err = s.expandedDepth(child, active, memo)
				delete(active, ref)
				if err != nil {
					return 0, err
				}
				memo[ref] = d
			}
			if d > maxDepth {
				maxDepth = d
			}
		case "properties", "$defs":
			children, _ := wireObject(v)
			for _, child := range children {
				c, _ := wireObject(child)
				if err := visit(c); err != nil {
					return 0, err
				}
			}
		case "items", "if", "then", "else", "not":
			c, _ := wireObject(v)
			if err := visit(c); err != nil {
				return 0, err
			}
		case "oneOf", "anyOf", "allOf":
			children, _ := wireArray(v)
			for _, child := range children {
				c, _ := wireObject(child)
				if err := visit(c); err != nil {
					return 0, err
				}
			}
		}
	}
	return maxDepth + 1, nil
}
