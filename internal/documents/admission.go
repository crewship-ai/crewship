package documents

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/storageheadroom"
)

// admitDisk is the same free-disk admission native Craft writes use. Call it
// inside the writer transaction after exact-retry replay and validation, and
// before the first growing insert, so a refusal writes nothing. Lease and
// engine-recovery bookkeeping update fixed-width columns and stay admitted.
func admitDisk(ctx context.Context, tx *sql.Tx, writeBytes ...int) error {
	total := int64(0)
	for _, n := range writeBytes {
		total += int64(n)
	}
	return storageheadroom.Check(ctx, tx, total)
}

// admitNewReceipt limits throughput, never the lifetime of idempotency evidence.
// Call only after the authorized exact-retry lookup. All existing receipts remain
// authoritative for the Page's lifetime, including after this window expires.
func (s *Store) admitNewReceipt(ctx context.Context, tx *sql.Tx, k Key) error {
	return s.admitReceiptWindow(ctx, tx, k, false)
}

// admitDiscussionReceipt is the same window for comment and resolve receipts.
// The two windows are separate, so discussion never makes an editor's save
// wait, and edits never make a comment wait.
func (s *Store) admitDiscussionReceipt(ctx context.Context, tx *sql.Tx, k Key) error {
	return s.admitReceiptWindow(ctx, tx, k, true)
}

func (s *Store) admitReceiptWindow(ctx context.Context, tx *sql.Tx, k Key, discussion bool) error {
	var count int
	// SQLite supplies the server time in the writer transaction. The indexed
	// range uses the same fixed-width UTC format as receipt defaults.
	modifier := fmt.Sprintf("-%.3f seconds", s.opts.ReceiptWindow.Seconds())
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM document_receipts WHERE workspace_id=? AND page_id=? AND created_at >= strftime('%Y-%m-%dT%H:%M:%fZ','now',?) AND created_at <= strftime('%Y-%m-%dT%H:%M:%fZ','now') AND (kind IN ('comment','resolve'))=?`, k.WorkspaceID, k.PageID, modifier, discussion).Scan(&count); err != nil {
		return err
	}
	if count >= s.opts.MaxCommits {
		return ErrRateLimit
	}
	return nil
}
