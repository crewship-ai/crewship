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
func (e *Engine) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if !e.config.SearchEnabled {
		return nil, fmt.Errorf("search is disabled")
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

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
		// Simple query: wrap each word in quotes for safety
		words := strings.Fields(q)
		quoted := make([]string, len(words))
		for i, w := range words {
			quoted[i] = "\"" + w + "\""
		}
		return strings.Join(quoted, " ")
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
	var parts []string
	for _, w := range words {
		upper := strings.ToUpper(w)
		if upper == "AND" || upper == "OR" || upper == "NOT" {
			parts = append(parts, upper)
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
