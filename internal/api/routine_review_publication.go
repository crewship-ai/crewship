package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// This is an internal publication hook, never a generic approval action. Store
// has consumed the exact tested draft and written the recipe/version in this
// transaction; a failed receipt, live reviewer check or audit rolls all of it back.
func (r *Router) finalizeRoutineDraftReviewTx(ctx context.Context, tx *sql.Tx, draft *pipeline.Draft, actor, routine string, version int) (func(), error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,payload,COALESCE(timeout_at,'') FROM approvals_queue WHERE workspace_id=? AND kind=? AND status='pending' AND json_extract(payload,'$.routine_id')=? AND json_extract(payload,'$.draft_id')=? ORDER BY created_at,id`, draft.WorkspaceID, harbormaster.KindRoutineDraftReview, routine, draft.ID)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, raw, deadline string }
	var reviews []candidate
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.id, &item.raw, &item.deadline); err != nil {
			break
		}
		reviews = append(reviews, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if len(reviews) == 0 {
		return nil, nil
	}
	allowed, err := routineReviewAuthorityIn(ctx, tx, draft.WorkspaceID, actor, false)
	if err != nil {
		return nil, err
	}
	if !allowed || version < 1 {
		return nil, fmt.Errorf("current trusted routine publisher is unavailable: %w", pipeline.ErrDraftConflict)
	}
	var publishedHash string
	if err = tx.QueryRowContext(ctx, `SELECT definition_hash FROM pipeline_versions WHERE pipeline_id=? AND version=?`, routine, version).Scan(&publishedHash); err != nil {
		return nil, err
	}
	var notifications []func()
	for _, item := range reviews {
		var payload routineDraftReviewPayload
		if err = json.Unmarshal([]byte(item.raw), &payload); err != nil {
			return nil, err
		}
		deadline, parseErr := time.Parse(time.RFC3339Nano, item.deadline)
		if parseErr != nil {
			return nil, parseErr
		}
		status, receiptVersion := harbormaster.StatusApproved, any(version)
		event, summary := journal.EntryApprovalGranted, "Tested routine draft published after review"
		if !deadline.After(time.Now()) {
			status, receiptVersion = harbormaster.StatusTimeout, nil
			event, summary = journal.EntryApprovalTimeout, "Routine draft review expired before publication"
		} else if payload.Revision != draft.Revision || payload.BaseRevision != draft.BaseRevision || payload.DocumentHash != pipeline.DefinitionHash(draft.Document) {
			status, receiptVersion = harbormaster.StatusCancelled, nil
			event, summary = journal.EntryApprovalCancelled, "Routine draft review superseded before publication"
		} else if payload.RoutineID != routine || payload.DraftID != draft.ID || payload.DefinitionHash != publishedHash {
			return nil, pipeline.ErrDraftConflict
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE approvals_queue SET status=?,decided_by=?,decided_at=?,decision_comment=?,routine_version=? WHERE id=? AND workspace_id=? AND kind=? AND status='pending'`, status, actor, tsformat.Format(time.Now()), summary, receiptVersion, item.id, draft.WorkspaceID, harbormaster.KindRoutineDraftReview)
		if updateErr != nil {
			return nil, updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil || count != 1 {
			return nil, pipeline.ErrDraftConflict
		}
		notify, auditErr := r.appendGuestIssueAudit(ctx, tx, journal.Entry{WorkspaceID: draft.WorkspaceID, Type: event, ActorType: journal.ActorUser, ActorID: actor, Summary: summary, Payload: map[string]any{"approval_id": item.id, "kind": harbormaster.KindRoutineDraftReview, "routine_id": routine, "draft_id": draft.ID, "draft_revision": payload.Revision, "published_version": receiptVersion}})
		if auditErr != nil {
			return nil, auditErr
		}
		if notify != nil {
			notifications = append(notifications, notify)
		}
	}
	return func() {
		for _, notify := range notifications {
			notify()
		}
	}, nil
}
