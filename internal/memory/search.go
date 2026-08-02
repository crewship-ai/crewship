package memory

import (
	"context"
	"fmt"
	"strings"
)

// SearchResult is a single match from the FTS5 index.
type SearchResult struct {
	File      string  `json:"file"`
	LineStart int     `json:"line_start"`
	LineEnd   int     `json:"line_end"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
}

// Search performs a BM25-ranked FTS5 search over indexed memory chunks.
// The query supports FTS5 query syntax (e.g. "foo AND bar", "foo OR bar",
// prefix queries "foo*").
//
// No e.mu RLock is held: SQLite is opened in WAL mode (see New) and
// Reindex's DELETE+INSERTs run inside a single transaction, so a
// concurrent search sees a consistent snapshot of the index — either
// the pre-reindex or post-reindex state, never an in-progress write.
// Close() racing with an in-flight search just propagates as a normal
// sql.DB error; no lock is needed to guard that path.
func (e *Engine) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if !e.config.SearchEnabled {
		return nil, fmt.Errorf("search is disabled")
	}

	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	// Sanitize query: escape double quotes, wrap terms for safety
	query = sanitizeFTSQuery(query)
	if query == "" {
		return nil, nil
	}

	rows, err := e.db.QueryContext(ctx, `
		SELECT file, content, rank
		FROM memory_chunks
		WHERE memory_chunks MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("fts5 search: %w", err)
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var content string
		if err := rows.Scan(&r.File, &content, &r.Score); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		// Truncate long snippets
		if len(content) > 300 {
			r.Snippet = content[:300] + "..."
		} else {
			r.Snippet = content
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

// sanitizeFTSQuery makes a user query safe for FTS5.
// It preserves quoted phrases and trailing wildcards while stripping
// dangerous FTS5 operators like column filters ({col}:), NEAR, etc.
//
// A plain question is turned into `"<phrase>" OR "t1" OR "t2" OR …` — see
// buildPhraseOrTerms. It used to be turned into `"t1" "t2" …`, and a space
// between two FTS5 terms is an implicit AND, so an eight-word question
// demanded all eight words inside one ~500-character chunk. Measured against
// the production engine, 8 of 10 realistic questions returned ZERO rows
// (#1678). The [MEMORY GAP] block tells a woken agent to search for the
// project it is picking up, so that was not a ranking defect — it was an
// instruction that could not succeed.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}

	// Strip dangerous FTS5 injection patterns: column filters, NEAR
	// These are the constructs that allow information disclosure.
	dangerousPatterns := []string{"{", "}", ":", "^", "~", "(", ")", "+"}
	hasDangerous := false
	for _, p := range dangerousPatterns {
		if strings.Contains(q, p) {
			hasDangerous = true
			break
		}
	}

	if !hasDangerous {
		// No dangerous characters. Check if the query uses explicit FTS5 syntax
		// (quoted phrases, operators, wildcards) — if so, pass through as-is.
		hasOperators := strings.Contains(q, " AND ") || strings.Contains(q, " OR ") ||
			strings.Contains(q, " NOT ") || strings.Contains(q, "\"") || strings.Contains(q, "*")
		if hasOperators {
			return q
		}
		return buildPhraseOrTerms(strings.Fields(q))
	}

	// Dangerous characters found — strip them and rebuild safely.
	// Extract quoted phrases first, then process remaining words.
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '{', '}', ':', '^', '~', '(', ')', '+':
			return ' '
		default:
			return r
		}
	}, q)

	words := strings.Fields(cleaned)

	// A query only becomes dangerous by containing one of the stripped
	// characters, which most natural questions do not — but "keeper: aux
	// slots" and "deploy (dev slot)" do, and they deserve the same
	// treatment as the same question without the punctuation. Only when a
	// real operator or wildcard survived the strip does the caller clearly
	// mean FTS5 syntax, and then the operator-preserving rebuild below
	// applies.
	if !hasExplicitFTSSyntax(words) {
		stripped := make([]string, 0, len(words))
		for _, w := range words {
			if w = strings.ReplaceAll(w, "\"", ""); w != "" {
				stripped = append(stripped, w)
			}
		}
		return buildPhraseOrTerms(stripped)
	}

	parts := make([]string, 0, len(words))
	for _, w := range words {
		switch {
		case strings.EqualFold(w, "AND"):
			parts = append(parts, "AND")
			continue
		case strings.EqualFold(w, "OR"):
			parts = append(parts, "OR")
			continue
		case strings.EqualFold(w, "NOT"):
			parts = append(parts, "NOT")
			continue
		}
		// Remove any internal quotes, re-wrap for safety
		w = strings.ReplaceAll(w, "\"", "")
		if w == "" {
			continue
		}
		// Preserve trailing wildcard
		if strings.HasSuffix(w, "*") {
			base := strings.TrimRight(w, "*")
			if base != "" {
				parts = append(parts, "\""+base+"\"*")
			}
		} else {
			parts = append(parts, "\""+w+"\"")
		}
	}
	return strings.Join(parts, " ")
}

// hasExplicitFTSSyntax reports whether the caller wrote FTS5 themselves —
// a boolean operator or a wildcard — rather than asking a question.
func hasExplicitFTSSyntax(words []string) bool {
	for _, w := range words {
		if strings.EqualFold(w, "AND") || strings.EqualFold(w, "OR") ||
			strings.EqualFold(w, "NOT") || strings.Contains(w, "*") {
			return true
		}
	}
	return false
}

// buildPhraseOrTerms turns the words of a plain question into
// `"<phrase>" OR "t1" OR "t2" OR …`.
//
// The phrase comes first so a chunk containing the whole question in order
// ranks at the top; the individual terms follow so a chunk containing only
// some of them still comes back at all. Everything is quoted: quoting is
// what keeps a word that happens to spell `OR` or `NEAR` from being parsed
// as an operator, and it is unchanged from the previous builder.
//
// Stopwords are dropped from the term list because a bare OR inherits the
// opposite failure from AND: "what did we decide about journal retention"
// OR-expands to include `what`, `did`, `we` and `about`, which match nearly
// every chunk and bury the two terms that carried the meaning. Measured on
// this repository's docs/ tree, dropping them moved top-3 accuracy from
// 7/10 to 8/10.
func buildPhraseOrTerms(words []string) string {
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.ReplaceAll(w, "\"", ""); w != "" {
			terms = append(terms, w)
		}
	}
	if len(terms) == 0 {
		return ""
	}

	kept := make([]string, 0, len(terms))
	for _, w := range terms {
		if !searchStopwords[strings.ToLower(w)] {
			kept = append(kept, w)
		}
	}
	// A question made ENTIRELY of stopwords ("what is the") must not become
	// an empty MATCH expression: FTS5 rejects one, and returning "" would
	// silently answer a real question with nothing. Fall back to the
	// unfiltered words — the result is bounded by the chunks that actually
	// contain them, which is the honest answer to a question with no
	// content words in it.
	if len(kept) == 0 {
		kept = terms
	}

	parts := make([]string, 0, len(kept)+1)
	if len(kept) > 1 {
		parts = append(parts, "\""+strings.Join(kept, " ")+"\"")
	}
	for _, w := range kept {
		parts = append(parts, "\""+w+"\"")
	}
	return strings.Join(parts, " OR ")
}

// searchStopwords are the highest-frequency function words in the two
// languages this product is used in, held as one flat bilingual set
// because the two share several spellings ("do", "to", "a").
//
// Deliberately short. The failure mode of a too-short list is a little
// noise in the OR expansion; the failure mode of a too-long one is a
// deleted content word and a lost answer — "how do I deploy" is a question,
// "deploy" is the answer, and a list that grew to include "slot" or "run"
// would start eating the query. It is not stemmed, not per-language, and
// not configurable: every one of those is a knob whose value cannot be
// judged without the read-side eval that does not exist yet
// (docs/prd/memory-retrieval-layer.md §8).
var searchStopwords = map[string]bool{
	// English
	"a": true, "about": true, "after": true, "an": true, "and": true,
	"any": true, "are": true, "as": true, "at": true, "be": true, "but": true,
	"by": true, "did": true, "do": true, "does": true, "for": true, "from": true,
	"how": true, "i": true, "if": true, "in": true, "is": true, "it": true,
	"of": true, "on": true, "or": true, "should": true, "that": true,
	"the": true, "then": true, "there": true, "this": true, "to": true,
	"was": true, "we": true, "were": true, "what": true, "when": true,
	"where": true, "which": true, "who": true, "why": true, "with": true,
	// Czech
	"aby": true, "ale": true, "ani": true, "až": true,
	"co": true, "další": true, "je": true, "jak": true,
	"jako": true, "jsem": true, "jsme": true, "jsou": true, "již": true,
	"k": true, "kde": true, "která": true, "které": true, "který": true,
	"na": true, "nebo": true, "než": true, "o": true, "po": true, "pro": true,
	"při": true, "s": true, "se": true, "si": true, "tak": true, "také": true,
	"u": true, "už": true, "v": true, "ve": true, "z": true,
	"za": true, "že": true,
}
