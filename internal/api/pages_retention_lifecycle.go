package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const pageMutationReplayWindow = 24 * time.Hour
const boardHistoryWindow = 24 * time.Hour

// Publication receipts for retained revisions remain useful for exact retries.
// Receipts alone never pin source bytes. Removed source has a 24-hour replay
// guarantee, independent of how many revisions a long-lived Page has published.
func compactArtifactReceipts(ctx context.Context, tx *sql.Tx, page string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM page_artifact_mutation_receipts WHERE page_id=? AND created_at<? AND NOT EXISTS(SELECT 1 FROM page_artifact_revisions r WHERE r.page_id=page_artifact_mutation_receipts.page_id AND r.revision=page_artifact_mutation_receipts.revision)`, page, tsformat.Format(now.Add(-pageMutationReplayWindow)))
	return err
}
