package api

import (
	"context"
	"database/sql"
	"errors"
)

var errBoardSourceHistoryCapacity = errors.New("board undo source history capacity reached")

type boardPlacementMode struct {
	ID             string `json:"id"`
	ExpectedRev    *int64 `json:"expected_rev"`
	Mode           string `json:"mode"`
	SourceRevision *int64 `json:"source_revision"`
}

func validBoardPlacementMode(v boardPlacementMode) bool {
	return boardMutationValid(v.ID) && v.ExpectedRev != nil && validBoardPlacementRevision(v.ExpectedRev) && v.SourceRevision != nil && *v.SourceRevision > 0 && *v.SourceRevision < 9007199254740991 && (v.Mode == "fixed" || v.Mode == "follow_latest")
}
func (h *PageHandler) updateBoardPlacementMode(ctx context.Context, tx *sql.Tx, page string, v boardPlacementMode, seq int64, mutation string) (int64, error) {
	rev, err := boardPlacementRevision(ctx, tx, page, v.ID, v.ExpectedRev, false)
	if err != nil {
		return 0, err
	}
	var source string
	var baseline int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(source_page_id,''),COALESCE(source_revision,0) FROM page_board_artifact_placements WHERE board_page_id=? AND id=?`, page, v.ID).Scan(&source, &baseline); err != nil {
		return 0, err
	}
	if source == "" {
		return 0, errBoardPlacementSource
	}
	var slug string
	if tx.QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, source, WorkspaceIDFromContext(ctx)).Scan(&slug) != nil {
		return 0, errBoardPlacementSource
	}
	rec, err := h.loadPage(ctx, WorkspaceIDFromContext(ctx), slug)
	if err != nil {
		return 0, err
	}
	viewer, err := h.loadViewer(ctx, WorkspaceIDFromContext(ctx), UserFromContext(ctx).ID)
	if err != nil {
		return 0, err
	}
	visible, err := h.canSeePage(ctx, WorkspaceIDFromContext(ctx), rec, nil, viewer)
	if err != nil {
		return 0, err
	}
	if !visible || !rec.HasArtifact {
		return 0, errBoardPlacementSource
	}
	var present int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=? AND revision=?`, source, *v.SourceRevision).Scan(&present); err != nil {
		return 0, err
	}
	if present != 1 {
		return 0, errors.Join(errBoardPlacementChanged, errors.New("requested source revision is no longer retained"))
	}
	if baseline != *v.SourceRevision {
		var retained int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM page_board_source_history WHERE board_page_id=?`, page).Scan(&retained); err != nil {
			return 0, err
		}
		if retained >= 10000 {
			return 0, errBoardSourceHistoryCapacity
		}

		if _, err = tx.ExecContext(ctx, `INSERT INTO page_board_source_history(board_page_id,mutation_id,placement_id,source_page_id,source_revision) VALUES(?,?,?,?,?)`, page, mutation, v.ID, source, baseline); err != nil {
			return 0, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE page_board_artifact_placements SET mode=?,source_revision=?,placement_rev=?,updated_seq=?,author_user_id=? WHERE board_page_id=? AND id=?`, v.Mode, *v.SourceRevision, rev+1, seq, UserFromContext(ctx).ID, page, v.ID)
	return rev + 1, err
}

// Resolve inside the caller's read/write snapshot. Following changes only the
// viewed revision, never placement identity, Board seq, baseline pin or grants.
func boardPlacementEffectiveRevision(ctx context.Context, q pageAuthQueryer, source string, baseline int64, mode string) (int64, error) {
	if mode == "fixed" {
		return baseline, nil
	}
	if mode != "follow_latest" {
		return 0, errors.New("invalid placement mode")
	}
	var head int64
	err := q.QueryRowContext(ctx, `SELECT head_rev FROM page_artifacts WHERE page_id=?`, source).Scan(&head)
	return head, err
}
