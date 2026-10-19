package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

type publicationDraftReference struct {
	DraftID      string `json:"draft_id"`
	Revision     int    `json:"revision"`
	BaseRevision int    `json:"base_revision"`
}

type routinePublicationValidationResponse struct {
	Valid        bool   `json:"valid"`
	DraftID      string `json:"draft_id"`
	Revision     int    `json:"revision"`
	BaseRevision int    `json:"base_revision"`
}

type routinePublicationResponse struct {
	ID                  string `json:"id"`
	Version             int    `json:"version"`
	PublicationRevision int    `json:"publication_revision"`
}

// Validation is a preview of the exact stored draft, never a test-run receipt
// or execution capability. Publication must revalidate inside its own writer.
func (r *Router) routinePublicationValidate(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		guestRoutineDraftError(w, access.ErrDenied)
		return
	}
	var reference publicationDraftReference
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxExecBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		replyError(w, 400, "Supply the current draft snapshot")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || reference.DraftID == "" || reference.Revision < 1 || reference.BaseRevision < 1 {
		replyError(w, 400, "Supply the current draft snapshot")
		return
	}
	tx, err := r.db.BeginTx(req.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	defer tx.Rollback()
	draft, content, err := r.publicationDraftIn(req.Context(), tx, user.ID, workspace, req.PathValue("routineId"), reference)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if err := restrictedworkflow.ValidatePublicationDefinition(string(content.Definition)); err != nil {
		replyError(w, 422, "Routine requires unsupported publication steps or resources")
		return
	}
	writeJSON(w, 200, routinePublicationValidationResponse{Valid: true, DraftID: draft.ID, Revision: draft.Revision, BaseRevision: draft.BaseRevision})
}

// Both doors resolve identities and the exact reviewed snapshot on the host.
func (r *Router) publicationDraftIn(ctx context.Context, tx *sql.Tx, user, workspace, id string, reference publicationDraftReference) (*pipeline.Draft, guestRoutineDraftContent, error) {
	var content guestRoutineDraftContent
	if _, err := guestRoutineMember(ctx, tx, user, workspace); err != nil {
		return nil, content, err
	}
	var routine, slug string
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT id,slug,publication_revision FROM pipelines WHERE workspace_id=? AND (id=? OR slug=?) AND deleted_at IS NULL`, workspace, id, id).Scan(&routine, &slug, &revision)
	if err != nil {
		return nil, content, err
	}
	store := access.Store{DB: r.db}
	for _, operation := range []string{"view", "edit"} {
		if err = store.CheckIn(ctx, tx, user, workspace, access.Right{Kind: "routine", ID: routine, Operation: operation}); err != nil {
			return nil, content, err
		}
	}
	draft, err := pipeline.NewStore(r.db).GetDraftIn(ctx, tx, workspace, slug)
	if err != nil {
		return nil, content, err
	}
	if draft.ID != reference.DraftID || draft.Revision != reference.Revision || draft.BaseRevision != reference.BaseRevision || draft.BasePipelineID != routine || draft.BaseRevision != revision {
		return nil, content, pipeline.ErrDraftConflict
	}
	if err := json.Unmarshal(draft.Document, &content); err != nil || !draftDefinitionObject(content.Definition) {
		return nil, content, errors.New("invalid stored draft")
	}
	return draft, content, nil
}

func (r *Router) routinePublicationPublish(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(err error) {
		if errors.Is(err, restrictedworkflow.ErrDenied) {
			err = access.ErrDenied
		}
		if errors.Is(err, restrictedworkflow.ErrUnsupported) {
			replyError(w, 422, "Routine requires unsupported publication steps or resources")
		} else {
			guestRoutineDraftError(w, err)
		}
	}
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		fail(access.ErrDenied)
		return
	}
	var reference publicationDraftReference
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxExecBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		replyError(w, 400, "Supply the current draft snapshot")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || reference.DraftID == "" || reference.Revision < 1 || reference.BaseRevision < 1 {
		replyError(w, 400, "Supply the current draft snapshot")
		return
	}
	// database.Open uses _txlock=immediate: acquire the writer before reading
	// authority or the draft. A busy writer remains a 500, never a CAS conflict.
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		fail(err)
		return
	}
	defer tx.Rollback()
	draft, content, err := r.publicationDraftIn(req.Context(), tx, user.ID, workspace, req.PathValue("routineId"), reference)
	if err != nil {
		fail(err)
		return
	}
	store := access.Store{DB: r.db}
	if err = store.CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "routine", ID: draft.BasePipelineID, Operation: "publish"}); err != nil {
		fail(err)
		return
	}
	_, canonical, fingerprint, err := restrictedworkflow.CompilePublicationIn(req.Context(), r.db, tx, user.ID, workspace, draft.BasePipelineID, string(content.Definition))
	if err != nil {
		fail(err)
		return
	}
	version, err := pipeline.NewStore(r.db).PublishDraftIn(req.Context(), tx, user.ID, *draft, canonical, fingerprint)
	if err != nil {
		fail(err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{
		WorkspaceID: workspace, Type: journal.EntryAuditEntityUpdated,
		ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest published a shared routine draft",
		Payload: map[string]any{"entity_type": "routine", "action": "publish", "pipeline_id": draft.BasePipelineID, "draft_id": draft.ID, "version": version},
	})
	if err != nil {
		fail(err)
		return
	}
	if err = tx.Commit(); err != nil {
		fail(err)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, routinePublicationResponse{ID: draft.BasePipelineID, Version: version, PublicationRevision: draft.BaseRevision + 1})
}
