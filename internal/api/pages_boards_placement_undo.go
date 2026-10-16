package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type boardPlacementRestore struct {
	ID          string `json:"id"`
	ExpectedRev *int64 `json:"expected_rev"`
}

var errBoardPlacementChanged = errors.New("placement changed or no longer exists")
var errBoardPlacementSource = errors.New("current source read permission required to restore placement")

func validBoardPlacementRevision(rev *int64) bool {
	return rev == nil || (*rev > 0 && *rev < 9007199254740991)
}
func boardPlacementRevision(ctx context.Context, tx *sql.Tx, pageID, id string, expected *int64, deleted bool) (int64, error) {
	var rev int64
	var removed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT placement_rev,deleted_at FROM page_board_artifact_placements WHERE board_page_id=? AND id=?`, pageID, id).Scan(&rev, &removed); errors.Is(err, sql.ErrNoRows) {
		return 0, errBoardPlacementChanged
	} else if err != nil {
		return 0, err
	}
	if !validBoardPlacementRevision(expected) || rev < 1 || rev >= 9007199254740991 || removed.Valid != deleted || (expected != nil && *expected != rev) {
		return 0, errBoardPlacementChanged
	}
	return rev, nil
}
func (h *PageHandler) updateBoardPlacement(ctx context.Context, tx *sql.Tx, pageID, id string, expected *int64, geometry boardPlacementGeometry, seq int64) (int64, error) {
	rev, err := boardPlacementRevision(ctx, tx, pageID, id, expected, false)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_artifact_placements SET x=?,y=?,width=?,height=?,updated_seq=?,author_user_id=?,placement_rev=? WHERE board_page_id=? AND id=?`, *geometry.X, *geometry.Y, *geometry.Width, *geometry.Height, seq, UserFromContext(ctx).ID, rev+1, pageID, id)
	return rev + 1, err
}
func (h *PageHandler) deleteBoardPlacement(ctx context.Context, tx *sql.Tx, pageID, id string, expected *int64, seq int64) (int64, error) {
	rev, err := boardPlacementRevision(ctx, tx, pageID, id, expected, false)
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_artifact_placements SET deleted_at=?,updated_seq=?,author_user_id=?,placement_rev=? WHERE board_page_id=? AND id=?`, tsformat.Format(h.evaluator().Now()), seq, UserFromContext(ctx).ID, rev+1, pageID, id)
	return rev + 1, err
}
func (h *PageHandler) restoreBoardPlacement(ctx context.Context, tx *sql.Tx, pageID, id string, expected *int64, seq int64) (int64, error) {
	if expected == nil {
		return 0, errBoardPlacementChanged
	}
	rev, err := boardPlacementRevision(ctx, tx, pageID, id, expected, true)
	if err != nil {
		return 0, err
	}
	var source string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(source_page_id,'') FROM page_board_artifact_placements WHERE board_page_id=? AND id=?`, pageID, id).Scan(&source); err != nil {
		return 0, err
	}
	if source != "" {
		workspace := WorkspaceIDFromContext(ctx)
		var slug string
		if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, source, workspace).Scan(&slug) != nil {
			return 0, errBoardPlacementSource
		}
		rec, err := h.loadPage(ctx, workspace, slug)
		if err != nil {
			return 0, err
		}
		viewer, err := h.loadViewer(ctx, workspace, UserFromContext(ctx).ID)
		if err != nil {
			return 0, err
		}
		visible, err := h.canSeePage(ctx, workspace, rec, nil, viewer)
		if err != nil {
			return 0, err
		}
		if !visible {
			return 0, errBoardPlacementSource
		}
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NULL`, pageID).Scan(&count); err != nil {
		return 0, err
	}
	if count >= 100 {
		return 0, errors.New("board placement limit reached")
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_artifact_placements SET deleted_at=NULL,updated_seq=?,author_user_id=?,placement_rev=? WHERE board_page_id=? AND id=?`, seq, UserFromContext(ctx).ID, rev+1, pageID, id)
	return rev + 1, err
}
func recordBoardPlacementRevisions(ctx context.Context, tx *sql.Tx, pageID, mutation string, revisions map[string]int64) error {
	raw, err := json.Marshal(revisions)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_mutations SET placement_revs_json=? WHERE board_page_id=? AND mutation_id=?`, string(raw), pageID, mutation)
	return err
}
func loadBoardPlacementTombstones(ctx context.Context, q pageAuthQueryer, pageID string) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,placement_rev FROM page_board_artifact_placements WHERE board_page_id=? AND deleted_at IS NOT NULL ORDER BY id LIMIT 1001`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		var rev int64
		if err = rows.Scan(&id, &rev); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "placement_rev": rev, "deleted": true})
		if len(result) > 1000 {
			return nil, errors.New("board placement record limit exceeded")
		}
	}
	return result, rows.Err()
}

func (h *PageHandler) boardPlacementError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBoardPlacementChanged) {
		replyError(w, 409, err.Error())
	} else if errors.Is(err, errBoardPlacementSource) {
		replyError(w, 403, err.Error())
	} else {
		h.boardError(w, err)
	}
}
