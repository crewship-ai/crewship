package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const boardHistoryWindow = 24 * time.Hour

var errBoardHistoryExpired = errors.New("board retry history expired; reload before creating a new mutation")

func boardUndoExpiry(stamp string) string {
	t, e := time.Parse(time.RFC3339Nano, stamp) // tsformat:allow: parsing original receipt time; Format writes canonical deadline
	if e != nil {
		return ""
	}
	return tsformat.Format(t.Add(boardHistoryWindow))
}
func (h *PageHandler) compactBoardHistory(ctx context.Context, tx *sql.Tx, page string) error {
	cutoff := tsformat.Format(h.evaluator().Now().Add(-boardHistoryWindow))
	// Older backups can contain replay receipts without the later audit table.
	// Copy their metadata in this same transaction before any receipt is removed.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO page_board_write_audit(board_page_id,mutation_id,actor_user_id,actor_agent_id,actor_run_id,result_seq,request_digest,created_at) SELECT board_page_id,mutation_id,author_user_id,author_agent_id,author_run_id,result_seq,fingerprint,created_at FROM page_board_mutations WHERE board_page_id=? AND created_at<?`, page, cutoff); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM page_board_mutations WHERE board_page_id=? AND created_at<?`, `DELETE FROM page_board_elements WHERE board_page_id=? AND element_json IS NULL AND deleted_at<?`, `DELETE FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at<?`} {
		if _, err := tx.ExecContext(ctx, q, page, cutoff); err != nil {
			return err
		}
	}
	return nil
}
func (h *PageHandler) recordBoardElementRevisions(ctx context.Context, tx *sql.Tx, page, mutation string, revs map[string]int64) error {
	raw, err := json.Marshal(revs)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_mutations SET element_revs_json=? WHERE board_page_id=? AND mutation_id=?`, string(raw), page, mutation)
	return err
}
func boardHistoryExpired(ctx context.Context, q pageAuthQueryer, page, mutation string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_write_audit WHERE board_page_id=? AND mutation_id=?`, page, mutation).Scan(&n)
	return n > 0, err
}
func (h *PageHandler) boardHistoryCapacity(w http.ResponseWriter, r *http.Request, tx *sql.Tx, page string, sourceOnly ...bool) {
	var expiry sql.NullString
	if len(sourceOnly) > 0 && sourceOnly[0] {
		_ = tx.QueryRowContext(r.Context(), `SELECT MIN(m.created_at) FROM page_board_source_history h JOIN page_board_mutations m ON m.board_page_id=h.board_page_id AND m.mutation_id=h.mutation_id WHERE h.board_page_id=?`, page).Scan(&expiry)
	} else {
		_ = tx.QueryRowContext(r.Context(), `SELECT MIN(stamp) FROM (SELECT deleted_at stamp FROM page_board_elements WHERE board_page_id=? AND deleted_at IS NOT NULL UNION ALL SELECT deleted_at stamp FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NOT NULL)`, page, page).Scan(&expiry)
	}

	writeJSON(w, 409, map[string]any{"error": "recent Board history is full; retry after history expires or reduce the active scene", "code": "history_capacity", "history_window_seconds": 86400, "earliest_history_expiry": boardUndoExpiry(expiry.String)})
}

func boardReceiptUndoExpiry(ctx context.Context, q pageAuthQueryer, page, mutation string) (string, error) {
	var stamp string
	err := q.QueryRowContext(ctx, `SELECT created_at FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, page, mutation).Scan(&stamp)
	if err != nil {
		return "", err
	}
	return boardUndoExpiry(stamp), nil
}
