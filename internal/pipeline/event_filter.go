package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Event filter syntax (wait.event_filter, webhook correlation path).
//
//	filter := term { "&&" term }
//	term   := path "==" value
//	path   := segment { "." segment }     segment: [A-Za-z0-9_-]+ (object key or array index)
//	value  := bare text (trimmed) | "double-quoted JSON string"
//
// The signal payload must be a JSON document; every term must hold for the
// signal to match (logical AND, no OR, no negation). A scalar at the path is
// compared by its text form: strings as-is, numbers as written, true/false,
// null. A missing path, a non-scalar, or a non-JSON payload never matches.
// An empty filter matches every signal.

type eventFilterTerm struct {
	path  []string
	value string
}

var templateRefRE = regexp.MustCompile(`\{\{.*?\}\}`)

// ValidateEventFilter checks syntax at save time. Template references are
// substituted with a placeholder first, since their values exist only at run time.
func ValidateEventFilter(filter string) error {
	_, err := parseEventFilter(templateRefRE.ReplaceAllString(filter, "x"))
	return err
}

// ValidateJSONPath checks a dotted payload path (webhook correlation extraction).
func ValidateJSONPath(p string) error {
	_, err := parsePayloadPath(p)
	return err
}

func parsePayloadPath(p string) ([]string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	segs := strings.Split(p, ".")
	for _, s := range segs {
		if s == "" {
			return nil, fmt.Errorf("path %q has an empty segment", p)
		}
		for _, c := range s {
			if !(c == '_' || c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
				return nil, fmt.Errorf("path %q: invalid character %q (allowed: letters, digits, _ - and . between segments)", p, c)
			}
		}
	}
	return segs, nil
}

// splitFilterTerms splits on "&&" outside double quotes.
func splitFilterTerms(f string) ([]string, error) {
	var terms []string
	start, inQuote := 0, false
	for i := 0; i < len(f); i++ {
		switch {
		case f[i] == '\\' && inQuote:
			i++
		case f[i] == '"':
			inQuote = !inQuote
		case !inQuote && f[i] == '&' && i+1 < len(f) && f[i+1] == '&':
			terms = append(terms, f[start:i])
			i++
			start = i + 1
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote")
	}
	return append(terms, f[start:]), nil
}

func parseEventFilter(filter string) ([]eventFilterTerm, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, nil
	}
	raw, err := splitFilterTerms(filter)
	if err != nil {
		return nil, fmt.Errorf("event_filter: %w", err)
	}
	out := make([]eventFilterTerm, 0, len(raw))
	for _, t := range raw {
		lhs, rhs, ok := strings.Cut(t, "==")
		if !ok {
			return nil, fmt.Errorf("event_filter: term %q must be `path == value`", strings.TrimSpace(t))
		}
		path, err := parsePayloadPath(lhs)
		if err != nil {
			return nil, fmt.Errorf("event_filter: %w", err)
		}
		val := strings.TrimSpace(rhs)
		if strings.HasPrefix(val, `"`) {
			var s string
			if err := json.Unmarshal([]byte(val), &s); err != nil {
				return nil, fmt.Errorf("event_filter: term %q: bad quoted value: %v", strings.TrimSpace(t), err)
			}
			val = s
		} else if val == "" || strings.Contains(val, "==") || strings.Contains(val, `"`) {
			return nil, fmt.Errorf("event_filter: term %q needs a bare value without quotes or `==`, or a quoted one (use \"\" for an empty string)", strings.TrimSpace(t))
		}
		out = append(out, eventFilterTerm{path: path, value: val})
	}
	return out, nil
}

// lookupPayloadScalar walks path in the JSON payload and returns the scalar's text form.
func lookupPayloadScalar(payload string, path []string) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
	dec.UseNumber()
	var cur any
	if dec.Decode(&cur) != nil {
		return "", false
	}
	for _, seg := range path {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return "", false
			}
			cur = v[i]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		return strconv.FormatBool(v), true
	case nil:
		return "null", true
	}
	return "", false
}

// EventFilterMatches reports whether payload satisfies the (already rendered)
// filter. A malformed filter never matches (fail closed).
func EventFilterMatches(filter, payload string) bool {
	terms, err := parseEventFilter(filter)
	if err != nil {
		return false
	}
	for _, t := range terms {
		got, ok := lookupPayloadScalar(payload, t.path)
		if !ok || got != t.value {
			return false
		}
	}
	return true
}

// ExtractPayloadScalar returns the scalar at the dotted path in a JSON payload.
func ExtractPayloadScalar(payload, path string) (string, bool) {
	segs, err := parsePayloadPath(path)
	if err != nil {
		return "", false
	}
	return lookupPayloadScalar(payload, segs)
}
