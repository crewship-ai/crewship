package pagesearch

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
)

type Query struct{ terms []string }

func ParseQuery(raw string) (Query, error) {
	if len(raw) > MaxQueryBytes || !validText(raw) {
		return Query{}, ErrBudget
	}
	terms := strings.Fields(cases.Fold().String(raw))
	if len(terms) == 0 || len(terms) > MaxQueryTerms {
		return Query{}, ErrInvalid
	}
	distinct := make([]string, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		if !seen[term] {
			seen[term] = true
			distinct = append(distinct, term)
		}
	}
	return Query{terms: distinct}, nil
}

// Match requires all query terms anywhere across this Page's leaf segments,
// then returns one snippet near the first matching term in source order.
// It performs no global ranking; callers authorize candidate Pages first.
func (q Query) Match(projection Projection) (string, bool, error) {
	if len(q.terms) == 0 {
		return "", false, ErrInvalid
	}
	if err := projection.Validate(); err != nil {
		return "", false, err
	}
	folder := cases.Fold()
	found := make([]bool, len(q.terms))
	firstSegment, firstPosition := -1, -1
	for segmentIndex, segment := range projection.Segments {
		folded := folder.String(segment.Text)
		first := -1
		for i, term := range q.terms {
			position := strings.Index(folded, term)
			if position >= 0 {
				found[i] = true
				if first < 0 || position < first {
					first = position
				}
			}
		}
		if firstSegment < 0 && first >= 0 {
			firstSegment, firstPosition = segmentIndex, originalPosition(segment.Text, first)
		}
	}
	for _, present := range found {
		if !present {
			return "", false, nil
		}
	}
	return snippet(projection.Segments[firstSegment].Text, firstPosition), true, nil
}

// Unicode case folding can expand a rune (ß→ss), so a folded byte offset is
// never used directly to slice original UTF-8. No proportional offset map is
// allocated; at most the matching segment prefix is traversed.
func originalPosition(text string, foldedPosition int) int {
	offset := 0
	folder := cases.Fold()
	for position, r := range text {
		size := 1
		if r >= utf8.RuneSelf {
			size = len(folder.String(string(r)))
		}
		if foldedPosition < offset+size {
			return position
		}
		offset += size
	}
	return len(text)
}

func snippet(text string, position int) string {
	start := max(0, position-128)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	end := min(len(text), start+MaxSnippetBytes)
	for end < len(text) && end > start && !utf8.RuneStart(text[end]) {
		end--
	}
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text[start:end])
}
