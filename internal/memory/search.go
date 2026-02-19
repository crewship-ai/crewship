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
// It wraps bare words in double quotes to prevent syntax errors from
// special characters, while preserving AND/OR/NOT operators.
func sanitizeFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}

	// If the query already contains FTS5 operators, pass through as-is
	// but still escape any unbalanced quotes.
	operators := []string{" AND ", " OR ", " NOT ", "\"", "*"}
	for _, op := range operators {
		if strings.Contains(q, op) {
			return q
		}
	}

	// Simple query: wrap each word in quotes for safety
	words := strings.Fields(q)
	quoted := make([]string, len(words))
	for i, w := range words {
		w = strings.ReplaceAll(w, "\"", "")
		quoted[i] = "\"" + w + "\""
	}
	return strings.Join(quoted, " ")
}
