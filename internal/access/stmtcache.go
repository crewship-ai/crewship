package access

import (
	"context"
	"database/sql"
)

// txStmtCache prepares each distinct SQL text once per transaction. Context
// reads re-check every recalled entry's origin authority, so one BuildContext
// runs the same few authority queries hundreds of times; with the pure-Go
// SQLite driver, parsing them again dominated the cost. Statements, arguments
// and the transaction are unchanged, so results and isolation are identical.
type txStmtCache struct {
	tx    *sql.Tx
	stmts map[string]*sql.Stmt
}

func newTxStmtCache(tx *sql.Tx) *txStmtCache {
	return &txStmtCache{tx: tx, stmts: map[string]*sql.Stmt{}}
}

func (c *txStmtCache) stmt(ctx context.Context, query string) (*sql.Stmt, error) {
	if st, ok := c.stmts[query]; ok {
		return st, nil
	}
	st, err := c.tx.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	c.stmts[query] = st
	return st, nil
}

func (c *txStmtCache) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	st, err := c.stmt(ctx, query)
	if err != nil {
		// *sql.Row cannot carry an error from outside database/sql; the
		// uncached path reports the same prepare failure from Scan.
		return c.tx.QueryRowContext(ctx, query, args...)
	}
	return st.QueryRowContext(ctx, args...)
}

func (c *txStmtCache) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	st, err := c.stmt(ctx, query)
	if err != nil {
		return nil, err
	}
	return st.QueryContext(ctx, args...)
}

func (c *txStmtCache) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	st, err := c.stmt(ctx, query)
	if err != nil {
		return nil, err
	}
	return st.ExecContext(ctx, args...)
}

// Close releases the prepared statements. Committing or rolling back the
// transaction also closes them; Close keeps that explicit and idempotent.
func (c *txStmtCache) Close() {
	for query, st := range c.stmts {
		st.Close()
		delete(c.stmts, query)
	}
}
