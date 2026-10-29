// Package literalfilter provides conservative necessary-literal checks for
// curated regex rules. It never proves a match; true always runs the regex.
package literalfilter

import "strings"

// Filter is immutable. Every group must have at least one literal present.
// The zero value is unknown, so custom rules always run their regex.
type Filter struct {
	groups [][]string
	folded bool
}

func ExactAll(literals ...string) Filter {
	groups := make([][]string, len(literals))
	for i, literal := range literals {
		groups[i] = []string{literal}
	}
	return Filter{groups: groups}
}

func ExactAny(literals ...string) Filter {
	if len(literals) == 0 {
		return Filter{}
	}
	return Filter{groups: [][]string{append([]string(nil), literals...)}}
}

func FoldedAll(literals ...string) Filter {
	normalized, ok := foldedLiterals(literals)
	if !ok {
		return Filter{}
	}
	f := ExactAll(normalized...)
	f.folded = true
	return f
}

func FoldedAny(literals ...string) Filter {
	normalized, ok := foldedLiterals(literals)
	if !ok {
		return Filter{}
	}
	f := ExactAny(normalized...)
	f.folded = true
	return f
}

// Folded filters are intentionally ASCII-only. Unsupported metadata disables
// rejection rather than inventing Unicode case-fold semantics.
func foldedLiterals(literals []string) ([]string, bool) {
	out := make([]string, len(literals))
	for i, literal := range literals {
		for j := 0; j < len(literal); j++ {
			if literal[j] >= 128 {
				return nil, false
			}
		}
		out[i] = strings.ToLower(literal)
	}
	return out, true
}

// View caches checks for one exact text view. Recreate it after a redaction,
// normalization or decoding step changes the text. It is local to one scan.
type View struct {
	text         string
	lower        string
	foldedReady  bool
	nonASCIIFold bool
}

func NewView(text string) View { return View{text: text} }

func (v *View) MayMatch(f Filter) bool {
	if len(f.groups) == 0 {
		return true
	}
	text := v.text
	if f.folded {
		if !v.foldedReady {
			// An already-present exact spelling is sufficient to run the regex.
			// Avoid normalizing a large input just to confirm an early positive rule.
			if containsGroups(v.text, f.groups) {
				return true
			}
			// These are the only non-ASCII members of ASCII SimpleFold cycles.
			// Do not use a byte-literal rejection for case-insensitive rules then.
			v.nonASCIIFold = strings.Contains(v.text, "ſ") || strings.Contains(v.text, "K")
			if !v.nonASCIIFold {
				v.lower = strings.ToLower(v.text)
			}
			v.foldedReady = true
		}
		if v.nonASCIIFold {
			return true
		}
		text = v.lower
	}
	return containsGroups(text, f.groups)
}

func containsGroups(text string, groups [][]string) bool {
	for _, group := range groups {
		found := false
		for _, literal := range group {
			if strings.Contains(text, literal) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
