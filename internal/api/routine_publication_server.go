package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type publicationDraftReference struct {
	DraftID      string `json:"draft_id"`
	Revision     int    `json:"revision"`
	BaseRevision int    `json:"base_revision"`
}

type routinePublicationValidationResponse struct {
	Valid bool   `json:"valid"`
	Scope string `json:"scope"`
}

type routinePublicationResponse struct {
	ID                  string `json:"id"`
	Version             int    `json:"version"`
	PublicationRevision int    `json:"publication_revision"`
}

func decodePublicationReference(w http.ResponseWriter, req *http.Request) (publicationDraftReference, bool) {
	var reference publicationDraftReference
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxExecBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		replyError(w, 400, "Supply the current draft snapshot")
		return reference, false
	}
	if err := decoder.Decode(new(any)); err != io.EOF || reference.DraftID == "" || reference.Revision < 1 || reference.BaseRevision < 1 {
		replyError(w, 400, "Supply the current draft snapshot")
		return reference, false
	}
	return reference, true
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
	reference, ok := decodePublicationReference(w, req)
	if !ok {
		return
	}
	tx, err := r.db.BeginTx(req.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	defer tx.Rollback()
	_, content, err := r.publicationDraftIn(req.Context(), tx, user.ID, workspace, req.PathValue("routineId"), reference)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if err := restrictedworkflow.ValidatePublicationDefinition(string(content.Definition)); err != nil {
		replyError(w, 422, "Routine requires unsupported publication steps or resources")
		return
	}
	writeJSON(w, 200, routinePublicationValidationResponse{Valid: true, Scope: "structure_only"})
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
		if errors.Is(err, pipeline.ErrPublicationHasTriggers) {
			replyError(w, http.StatusUnprocessableEntity, "Remove existing routine triggers before publishing this v1 routine")
			return
		}
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
	reference, ok := decodePublicationReference(w, req)
	if !ok {
		return
	}
	// database.Open uses _txlock=immediate: acquire the writer before reading
	// authority or the draft. Write contention returns 409 without retrying or
	// consuming the caller's reviewed snapshot.
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
	authority, canonical, fingerprint, err := restrictedworkflow.CompilePublicationIn(req.Context(), r.db, tx, user.ID, workspace, draft.BasePipelineID, string(content.Definition))
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
	// The configured audit emitter runs inside this transaction. Its native
	// journal ACK covers the event, so verify the publication business state
	// after that final write as well, before acknowledging it to the caller.
	var retained bool
	err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(
 SELECT 1 FROM pipelines p JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=?
 WHERE p.id=? AND p.workspace_id=? AND p.slug=? AND p.deleted_at IS NULL AND p.publication_withdrawn_at IS NULL
 AND p.head_version=? AND p.publication_revision=? AND p.name=? AND p.description IS ?
 AND p.definition_json=? AND p.definition_hash=? AND p.publication_authority_fingerprint=?
 AND p.author_crew_id IS NULLIF(?,'') AND p.author_agent_id IS NULLIF(?,'') AND p.chat_origin_id IS NULLIF(?,'')
 AND v.definition_json=p.definition_json AND v.definition_hash=p.definition_hash
 AND v.authority_fingerprint=? AND v.publication_authority_json=? AND v.author_type='user' AND v.author_id=?
 AND v.chat_origin_id IS p.chat_origin_id)
 AND NOT EXISTS(SELECT 1 FROM pipeline_drafts WHERE id=? OR (workspace_id=? AND slug=?))`,
		version, draft.BasePipelineID, workspace, draft.Slug, version, draft.BaseRevision+1, content.Name, content.Description,
		string(content.Definition), definitionHashHex(content.Definition), fingerprint, authority.RootCrewID, authority.AuthorAgentID, authority.RootOriginID,
		fingerprint, string(canonical), user.ID, draft.ID, workspace, draft.Slug).Scan(&retained)
	if err != nil || !retained {
		if err == nil {
			err = errors.New("publication state was not retained")
		}
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

type routineUnpublicationResponse struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	WithdrawnAt string `json:"withdrawn_at"`
}

// routinePublicationUnpublish withdraws the current Guest publication. The
// archive and manifest stay immutable; new runs are refused and running ones
// stop at their next step. A Publish holder or a trusted OWNER/ADMIN may act.
func (r *Router) routinePublicationUnpublish(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		guestRoutineDraftError(w, access.ErrDenied)
		return
	}
	if req.ContentLength > 0 {
		replyError(w, 400, "Unpublish takes no request body")
		return
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	defer tx.Rollback()
	var routine, fingerprint string
	var head int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(req.Context(), `SELECT id,publication_authority_fingerprint,head_version,publication_withdrawn_at FROM pipelines WHERE workspace_id=? AND (id=? OR slug=?) AND deleted_at IS NULL`, workspace, req.PathValue("routineId"), req.PathValue("routineId")).Scan(&routine, &fingerprint, &head, &withdrawn)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if _, err = invitationIssuerRole(req.Context(), tx, user.ID, workspace); errors.Is(err, sql.ErrNoRows) {
		err = (access.Store{DB: r.db}).CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "routine", ID: routine, Operation: "publish"})
	}
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if fingerprint == access.LegacyPublicationAuthorityFingerprint || withdrawn.Valid {
		replyError(w, 409, "Routine has no current Guest publication to withdraw")
		return
	}
	stamp := tsformat.Format(time.Now())
	result, err := tx.ExecContext(req.Context(), `UPDATE pipelines SET publication_withdrawn_at=? WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND publication_withdrawn_at IS NULL AND publication_authority_fingerprint=? AND head_version=?`, stamp, routine, workspace, fingerprint, head)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		guestRoutineDraftError(w, errors.Join(err, pipeline.ErrDraftConflict))
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{
		WorkspaceID: workspace, Type: journal.EntryAuditEntityUpdated,
		ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Routine publication withdrawn",
		Payload: map[string]any{"entity_type": "routine", "action": "unpublish", "pipeline_id": routine, "version": head, "authority_fingerprint": fingerprint},
	})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	var retained bool
	err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND publication_withdrawn_at=? AND publication_authority_fingerprint=? AND head_version=?)`, routine, workspace, stamp, fingerprint, head).Scan(&retained)
	if err != nil || !retained {
		guestRoutineDraftError(w, errors.Join(err, errors.New("withdrawal was not retained")))
		return
	}
	if err = tx.Commit(); err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, routineUnpublicationResponse{ID: routine, Version: head, WithdrawnAt: stamp})
}
