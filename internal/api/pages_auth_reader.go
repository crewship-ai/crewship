package api

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The existing Pages ACL readers can use the mutation's transaction. No shared
// handler state changes: ownership, live grants and folder ACLs are evaluated
// after the SQLite writer fence, so revocation and publication have one order.
type pageAuthReaderKey struct{}
type pageAuthQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (h *PageHandler) pageAuthReader(ctx context.Context) pageAuthQueryer {
	if tx, ok := ctx.Value(pageAuthReaderKey{}).(*sql.Tx); ok && tx != nil {
		return tx
	}
	return h.db
}

// Private output schemas are installed independently. Only known extension
// tables participate in custody; absent features never make ordinary Page
// departure or listing fail. A present unsupported schema fails closed.
func privatePageOwnershipSQL(ctx context.Context, q pageAuthQueryer, alias string) (string, string, error) {
	terms := []string{}
	cases := []string{}
	for _, extension := range []struct{ table, kind string }{
		{"slides_heads", "slides"}, {"documents", "document"},
		{"page_boards", "board"}, {"page_artifacts", "artifact"},
	} {
		var schemaType string
		err := q.QueryRowContext(ctx, `SELECT type FROM sqlite_schema WHERE name=? COLLATE NOCASE AND type IN ('table','view')`, extension.table).Scan(&schemaType)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("discover private output schema: %w", err)
		}
		if schemaType != "table" {
			return "", "", fmt.Errorf("unsupported private output schema for %s", extension.table)
		}
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info(?) WHERE name='page_id')`, extension.table).Scan(&exists); err != nil {
			return "", "", fmt.Errorf("discover private output schema: %w", err)
		}
		if !exists {
			return "", "", fmt.Errorf("private output schema %s is missing page_id", extension.table)
		}
		// Names come exclusively from the fixed list above and internal aliases.
		term := "EXISTS(SELECT 1 FROM " + extension.table + " WHERE page_id=" + alias + ".id)"
		terms = append(terms, term)
		cases = append(cases, "WHEN "+term+" THEN '"+extension.kind+"'")
	}
	if len(terms) == 0 {
		return "0", "''", nil
	}
	return "(" + strings.Join(terms, " OR ") + ")", "CASE " + strings.Join(cases, " ") + " ELSE '' END", nil
}
