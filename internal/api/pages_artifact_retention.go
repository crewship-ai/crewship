package api

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var errArtifactRetentionPinned = errors.New("artifact retained revisions are pinned")
var errArtifactReceiptLimit = errors.New("artifact mutation receipt limit reached")

// Reserve one retained source slot. Discussion, request and restore provenance
// pin their exact source; mutation receipts alone do not retain source bytes.
func reserveArtifactRevision(ctx context.Context, tx *sql.Tx, pageID string, restoring ...int64) error {
	return reserveArtifactRevisionAt(ctx, tx, pageID, time.Now(), restoring...)
}
func reserveArtifactRevisionAt(ctx context.Context, tx *sql.Tx, pageID string, now time.Time, restoring ...int64) error {
	var receipts, retained int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_mutation_receipts WHERE page_id=?`, pageID).Scan(&receipts); err != nil {
		return err
	}
	if receipts >= 10000 {
		return errArtifactReceiptLimit
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, pageID).Scan(&retained); err != nil {
		return err
	}
	restoreTarget := int64(0)
	if len(restoring) > 0 {
		restoreTarget = restoring[0]
	}
	needed := retained - maxArtifactRevisions + 1
	if needed <= 0 {
		return nil
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision IN (
 SELECT r.revision FROM page_artifact_revisions r WHERE r.page_id=?
 AND r.revision != ?
 AND r.revision != (SELECT head_rev FROM page_artifacts WHERE page_id=r.page_id)
 AND NOT EXISTS(SELECT 1 FROM page_board_source_history h JOIN page_board_mutations m ON m.board_page_id=h.board_page_id AND m.mutation_id=h.mutation_id WHERE h.source_page_id=r.page_id AND h.source_revision=r.revision AND m.created_at>=?)
 AND NOT EXISTS(SELECT 1 FROM page_board_artifact_placements b WHERE b.source_page_id=r.page_id AND b.source_revision=r.revision AND (b.deleted_at IS NULL OR b.deleted_at>=?))
 AND NOT EXISTS(SELECT 1 FROM page_board_request_sources q WHERE q.source_page_id=r.page_id AND q.source_revision=r.revision)
 AND NOT EXISTS(SELECT 1 FROM page_board_requests q WHERE q.result_page_id=r.page_id AND q.result_revision=r.revision)
 AND NOT EXISTS(SELECT 1 FROM page_artifact_threads t WHERE t.page_id=r.page_id AND t.revision=r.revision)
 AND NOT EXISTS(SELECT 1 FROM page_artifact_requests q WHERE q.page_id=r.page_id AND (q.base_rev=r.revision OR q.result_revision=r.revision))
 AND NOT EXISTS(SELECT 1 FROM page_artifact_revisions s WHERE s.page_id=r.page_id AND s.restored_from=r.revision)
 AND NOT EXISTS(SELECT 1 FROM page_artifact_mutation_receipts m WHERE m.page_id=r.page_id AND m.restored_from=r.revision)
 AND NOT EXISTS(SELECT 1 FROM page_artifact_creations c WHERE c.page_id=r.page_id AND r.revision=1)
 ORDER BY r.revision LIMIT ?)`, pageID, pageID, restoreTarget, tsformat.Format(now.Add(-boardHistoryWindow)), tsformat.Format(now.Add(-boardHistoryWindow)), needed)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n < int64(needed) {
		return errArtifactRetentionPinned
	}
	return nil
}
