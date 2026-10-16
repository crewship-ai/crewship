package documents

import (
	"context"
	"database/sql"
	"fmt"
)

// admitNewReceipt limits throughput, never the lifetime of idempotency evidence.
// Call only after the authorized exact-retry lookup. All existing receipts remain
// authoritative for the Page's lifetime, including after this window expires.
func (s *Store) admitNewReceipt(ctx context.Context, tx *sql.Tx, k Key) error {
	var count int
	// SQLite supplies the server time in the writer transaction. The indexed
	// range uses the same fixed-width UTC format as receipt defaults.
	modifier := fmt.Sprintf("-%.3f seconds", s.opts.ReceiptWindow.Seconds())
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND created_at >= strftime('%Y-%m-%dT%H:%M:%fZ','now',?) AND created_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now')`, k.WorkspaceID, k.PageID, modifier).Scan(&count); err != nil {
		return err
	}
	if count >= s.opts.MaxCommits {
		return ErrRateLimit
	}
	return nil
}
