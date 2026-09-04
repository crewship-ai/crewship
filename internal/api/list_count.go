package api

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
)

// countListRows runs a SELECT COUNT(*) query and returns the single integer.
// The list handlers use it for X-Total-Count (writeListMeta): the count and
// the page share one WHERE clause, so the header can never disagree with the
// rows.
func countListRows(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// listSearchClause turns ?q= into " AND (col LIKE ? OR col LIKE ?)" over the
// given columns, case-insensitively, with the matching args. Empty when the
// request carries no q. The clause is meant to be appended to a WHERE that
// already has a predicate.
func listSearchClause(r *http.Request, cols ...string) (string, []any) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(cols) == 0 {
		return "", nil
	}
	like := "%" + escapeLikeWildcards(strings.ToLower(q)) + "%"
	parts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, c := range cols {
		parts = append(parts, "LOWER(IFNULL("+c+", '')) LIKE ? ESCAPE '\\'")
		args = append(args, like)
	}
	return " AND (" + strings.Join(parts, " OR ") + ")", args
}
