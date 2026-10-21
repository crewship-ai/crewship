package inbox

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
)

// UpsertTx refreshes a subject's notice on the caller's transaction, including
// clearing its read/resolved state and per-user read markers. SQL failures must
// roll back the source mutation too. Like InsertTx, it performs no external
// fan-out before commit and rejects an incomplete transactional envelope.
func UpsertTx(ctx context.Context, tx *sql.Tx, logger *slog.Logger, in Item) error {
	if tx == nil {
		return errors.New("inbox: UpsertTx requires a transaction")
	}
	if in.WorkspaceID == "" || in.Kind == "" || in.SourceID == "" {
		return errors.New("inbox: UpsertTx requires workspace_id, kind and source_id")
	}
	if err := upsertRow(ctx, tx, in); err != nil {
		if logger != nil {
			logger.Warn("inbox transactional upsert", "error", err, "kind", in.Kind, "source_id", in.SourceID)
		}
		return err
	}
	return nil
}
