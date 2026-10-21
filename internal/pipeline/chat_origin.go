package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// bindSaveOrigin freezes active host provenance in the recipe transaction.
// Public edits without a fresh chat source preserve the current binding.
func bindSaveOrigin(ctx context.Context, tx *sql.Tx, in *SaveInput, pipelineID string) error {
	if in.SourceRunID != "" {
		o, err := runauthority.CaptureOrigin(ctx, tx, in.SourceRunID, in.SourceAgentID, in.WorkspaceID)
		if err != nil {
			return err
		}
		in.ChatOriginID = o.ID
	}
	if pipelineID != "" && in.ChatOriginID == "" {
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(chat_origin_id,'') FROM pipelines WHERE id=?`, pipelineID).Scan(&in.ChatOriginID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if in.SourceRunID == "" && in.Publication != nil {
		var origin string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(chat_origin_id,'') FROM pipeline_drafts WHERE id=? AND workspace_id=? AND revision=?`, in.Publication.ID, in.WorkspaceID, in.Publication.Revision).Scan(&origin); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrDraftConflict
			}
			return err
		}
		if origin != "" {
			in.ChatOriginID = origin
			if _, err := runauthority.ValidateOriginReceipt(ctx, tx, origin, in.WorkspaceID); err != nil {
				return err
			}
		}
	}
	if pipelineID != "" {
		_, err := tx.ExecContext(ctx, `UPDATE pipelines SET chat_origin_id=? WHERE id=?`, nullableStr(in.ChatOriginID), pipelineID)
		return err
	}
	return nil
}

func (e *Executor) bindRunOrigin(ctx context.Context, in *RunInput, p *Pipeline) error {
	if in.ChatOriginID == "" && !in.resume {
		var err error
		if in.PinnedVersion != nil {
			err = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(chat_origin_id,'') FROM pipeline_versions WHERE pipeline_id=? AND version=?`, p.ID, *in.PinnedVersion).Scan(&in.ChatOriginID)
		} else {
			err = e.store.db.QueryRowContext(ctx, `SELECT COALESCE(chat_origin_id,'') FROM pipelines WHERE id=?`, p.ID).Scan(&in.ChatOriginID)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("routine origin: %w", err)
		}
	}
	if in.ChatOriginID != "" {
		_, err := runauthority.ValidateOriginReceipt(ctx, e.store.db, in.ChatOriginID, in.WorkspaceID)
		if err != nil {
			return err
		}
	}
	return nil
}
