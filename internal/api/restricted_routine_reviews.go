package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type routineDraftReviewSnapshot struct {
	DraftID      string `json:"draft_id"`
	Revision     int    `json:"revision"`
	BaseRevision int    `json:"base_revision"`
}

type routineDraftReviewPayload struct {
	RoutineID      string `json:"routine_id"`
	Slug           string `json:"slug"`
	DraftID        string `json:"draft_id"`
	Revision       int    `json:"draft_revision"`
	BaseRevision   int    `json:"base_revision"`
	DocumentHash   string `json:"document_hash"`
	DefinitionHash string `json:"definition_hash"`
}

// No requester, reviewer, crew, origin or unpublished document is projected.
type guestRoutineDraftReview struct {
	ID           string `json:"id"`
	RoutineID    string `json:"routine_id"`
	DraftID      string `json:"draft_id"`
	Revision     int    `json:"revision"`
	BaseRevision int    `json:"base_revision"`
	State        string `json:"state"`
	Version      int    `json:"published_version"`
}

func (r *Router) restrictedRoutineDraftReview(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		guestRoutineDraftError(w, access.ErrDenied)
		return
	}
	write := req.Method == http.MethodPost
	var body routineDraftReviewSnapshot
	if write {
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			replyError(w, 400, "Invalid draft review snapshot")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			replyError(w, 400, "Invalid draft review snapshot")
			return
		}
		if body.DraftID == "" || body.Revision < 1 || body.BaseRevision < 0 {
			replyError(w, 400, "Save the draft before requesting publication review")
			return
		}
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	defer tx.Rollback()
	id, slug, base, err := r.guestRoutineDraftResourceIn(req.Context(), tx, user.ID, workspace, req.PathValue("routineId"))
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if !write {
		result, err := r.routineDraftReviewIn(req.Context(), tx, workspace, id, slug, req.PathValue("reviewId"))
		if errors.Is(err, sql.ErrNoRows) && req.PathValue("reviewId") == "" {
			writeJSON(w, 200, nil)
			return
		}
		if err != nil {
			guestRoutineDraftError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	draft, err := pipeline.NewStore(r.db).GetDraftIn(req.Context(), tx, workspace, slug)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if draft.ID != body.DraftID || draft.Revision != body.Revision || draft.BaseRevision != body.BaseRevision || draft.BaseRevision != base || draft.BasePipelineID != id {
		guestRoutineDraftError(w, pipeline.ErrDraftConflict)
		return
	}
	var existing string
	err = tx.QueryRowContext(req.Context(), `SELECT id FROM approvals_queue WHERE workspace_id=? AND kind=? AND json_extract(payload,'$.routine_id')=? AND json_extract(payload,'$.draft_id')=? AND json_extract(payload,'$.draft_revision')=? ORDER BY created_at DESC,id DESC LIMIT 1`, workspace, harbormaster.KindRoutineDraftReview, id, draft.ID, draft.Revision).Scan(&existing)
	if err == nil {
		result, readErr := r.routineDraftReviewIn(req.Context(), tx, workspace, id, slug, existing)
		if readErr != nil {
			guestRoutineDraftError(w, readErr)
			return
		}
		if result.State != "pending" {
			replyError(w, 409, "This draft review is no longer pending. Save a new revision before requesting another review.")
			return
		}
		writeJSON(w, 202, result)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		guestRoutineDraftError(w, err)
		return
	}
	var document struct {
		Definition json.RawMessage `json:"definition"`
	}
	if err = json.Unmarshal(draft.Document, &document); err != nil || !draftDefinitionObject(document.Definition) {
		guestRoutineDraftError(w, errors.New("invalid draft document"))
		return
	}
	payload := routineDraftReviewPayload{RoutineID: id, Slug: slug, DraftID: draft.ID, Revision: draft.Revision, BaseRevision: draft.BaseRevision, DocumentHash: pipeline.DefinitionHash(draft.Document), DefinitionHash: pipeline.DefinitionHash(document.Definition)}
	fields := map[string]any{"routine_id": payload.RoutineID, "slug": payload.Slug, "draft_id": payload.DraftID, "draft_revision": payload.Revision, "base_revision": payload.BaseRevision, "document_hash": payload.DocumentHash, "definition_hash": payload.DefinitionHash}
	superseded, err := r.supersedeRoutineDraftReviewsTx(req.Context(), tx, workspace, id, draft.ID, user.ID)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	reviewID, err := harbormaster.EnqueueTx(req.Context(), tx, harbormaster.Request{ID: generateCUID(), WorkspaceID: workspace, RequestedBy: user.ID, Kind: harbormaster.KindRoutineDraftReview, Reason: fmt.Sprintf("Review routine %s draft revision %d for publication", slug, draft.Revision), Payload: fields, TimeoutSecs: 7 * 24 * 60 * 60})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryApprovalRequest, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest requested routine draft publication review", Payload: map[string]any{"approval_id": reviewID, "kind": harbormaster.KindRoutineDraftReview, "routine_id": id, "draft_id": draft.ID, "revision": draft.Revision, "base_revision": draft.BaseRevision}})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if superseded != nil {
		superseded()
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 202, guestRoutineDraftReview{ID: reviewID, RoutineID: id, DraftID: draft.ID, Revision: draft.Revision, BaseRevision: draft.BaseRevision, State: "pending"})
}

func (r *Router) routineDraftReviewIn(ctx context.Context, tx *sql.Tx, workspace, routine, slug, review string) (*guestRoutineDraftReview, error) {
	var id, raw, status, timeout string
	var version int
	err := tx.QueryRowContext(ctx, `SELECT id,payload,status,COALESCE(timeout_at,''),COALESCE(routine_version,0) FROM approvals_queue WHERE workspace_id=? AND kind=? AND json_extract(payload,'$.routine_id')=? AND (?='' OR id=?) ORDER BY created_at DESC,id DESC LIMIT 1`, workspace, harbormaster.KindRoutineDraftReview, routine, review, review).Scan(&id, &raw, &status, &timeout, &version)
	if err != nil {
		return nil, err
	}
	var payload routineDraftReviewPayload
	if err = json.Unmarshal([]byte(raw), &payload); err != nil || payload.RoutineID != routine || payload.DraftID == "" || payload.Revision < 1 || len(payload.DocumentHash) != 64 || len(payload.DefinitionHash) != 64 {
		return nil, errors.New("invalid draft review receipt")
	}
	state := status
	if status == string(harbormaster.StatusApproved) {
		if version < 1 {
			return nil, errors.New("draft review has no published version")
		}
		state = "published"
	} else if status == string(harbormaster.StatusTimeout) {
		state = "expired"
	} else if status == string(harbormaster.StatusPending) {
		deadline, parseErr := time.Parse(time.RFC3339Nano, timeout)
		if parseErr != nil {
			return nil, parseErr
		}
		if !deadline.After(time.Now()) {
			state = "expired"
		} else {
			draft, readErr := pipeline.NewStore(r.db).GetDraftIn(ctx, tx, workspace, slug)
			if readErr != nil {
				return nil, readErr
			}
			var publishedBase int
			if readErr = tx.QueryRowContext(ctx, `SELECT publication_revision FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, routine, workspace).Scan(&publishedBase); readErr != nil {
				return nil, readErr
			}
			if draft.ID != payload.DraftID || draft.Revision != payload.Revision || draft.BaseRevision != payload.BaseRevision || publishedBase != payload.BaseRevision || pipeline.DefinitionHash(draft.Document) != payload.DocumentHash {
				state = "superseded"
			}
		}
	}
	return &guestRoutineDraftReview{ID: id, RoutineID: routine, DraftID: payload.DraftID, Revision: payload.Revision, BaseRevision: payload.BaseRevision, State: state, Version: version}, nil
}

// Submitting a saved newer revision replaces pending requests for the same
// collaborative draft. Cancellation, replacement and their audits share one
// transaction; an enqueue/audit failure preserves the original request.
func (r *Router) supersedeRoutineDraftReviewsTx(ctx context.Context, tx *sql.Tx, workspace, routine, draft, actor string) (func(), error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals_queue WHERE workspace_id=? AND kind=? AND status='pending' AND json_extract(payload,'$.routine_id')=? AND json_extract(payload,'$.draft_id')=?`, workspace, harbormaster.KindRoutineDraftReview, routine, draft)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	var notifications []func()
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE approvals_queue SET status='cancelled',decided_by=?,decided_at=?,decision_comment='Superseded by a new draft review request' WHERE id=? AND workspace_id=? AND kind=? AND status='pending'`, actor, tsformat.Format(time.Now()), id, workspace, harbormaster.KindRoutineDraftReview)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return nil, pipeline.ErrDraftConflict
		}
		notify, err := r.appendGuestIssueAudit(ctx, tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryApprovalCancelled, ActorType: journal.ActorUser, ActorID: actor, Summary: "Routine draft review superseded by a new revision", Payload: map[string]any{"approval_id": id, "kind": harbormaster.KindRoutineDraftReview, "routine_id": routine, "draft_id": draft}})
		if err != nil {
			return nil, err
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
