package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// A Guest edits the shared collaborative draft, never the live recipe. The
// trusted publication path must still test and review the exact draft revision.
// Author identities, trigger activation, origin and gate overrides are absent.
type guestRoutineDraftContent struct {
	DraftID      string          `json:"draft_id"`
	Revision     int             `json:"revision"`
	BaseRevision int             `json:"base_revision"`
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	Definition   json.RawMessage `json:"definition"`
}

type guestRoutineDraft struct {
	guestRoutineDraftContent
	CanEdit bool `json:"can_edit"`
}

func guestRoutineDraftError(w http.ResponseWriter, err error) {
	if errors.Is(err, pipeline.ErrDraftConflict) {
		replyError(w, 409, "Routine draft or published version changed. Reload and review before saving.")
		return
	}
	if errors.Is(err, access.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Routine draft unavailable")
		return
	}
	replyError(w, 500, "Routine draft unavailable")
}

func (r *Router) restrictedRoutineDraft(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		guestRoutineDraftError(w, access.ErrDenied)
		return
	}
	write := req.Method == http.MethodPut
	var body guestRoutineDraftContent
	if write {
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxExecBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			replyError(w, 400, "Invalid routine draft request")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			replyError(w, 400, "Invalid routine draft request")
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" || len(body.Name) > 4096 || body.Description == nil || len(*body.Description) > 65536 || body.Revision < 0 || body.BaseRevision < 0 || !draftDefinitionObject(body.Definition) {
			replyError(w, 400, "Supply name, description, a definition object and the current draft snapshot")
			return
		}
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = guestRoutineMember(req.Context(), tx, user.ID, workspace); err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	var routineID, slug string
	var publishedRevision int
	id := req.PathValue("routineId")
	err = tx.QueryRowContext(req.Context(), `SELECT id,slug,publication_revision FROM pipelines WHERE workspace_id=? AND (id=? OR slug=?) AND deleted_at IS NULL`, workspace, id, id).Scan(&routineID, &slug, &publishedRevision)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	store := access.Store{DB: r.db}
	for _, operation := range []string{"view", "edit"} {
		if err = store.CheckIn(req.Context(), tx, user.ID, workspace, access.Right{Kind: "routine", ID: routineID, Operation: operation}); err != nil {
			guestRoutineDraftError(w, err)
			return
		}
	}
	drafts := pipeline.NewStore(r.db)
	current, err := drafts.GetDraftIn(req.Context(), tx, workspace, slug)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if current.BasePipelineID != routineID || current.BaseRevision != publishedRevision {
		guestRoutineDraftError(w, pipeline.ErrDraftConflict)
		return
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(current.Document, &document); err != nil || document == nil {
		guestRoutineDraftError(w, errors.New("invalid stored draft"))
		return
	}
	var content guestRoutineDraftContent
	if err = json.Unmarshal(current.Document, &content); err != nil || !draftDefinitionObject(content.Definition) {
		guestRoutineDraftError(w, errors.New("invalid stored draft content"))
		return
	}
	content.DraftID, content.Revision, content.BaseRevision = current.ID, current.Revision, current.BaseRevision
	if content.Description == nil {
		empty := ""
		content.Description = &empty
	}
	if !write {
		writeJSON(w, 200, guestRoutineDraft{guestRoutineDraftContent: content, CanEdit: canScope(req.Context(), "workspace:admin")})
		return
	}
	if body.DraftID != current.ID || body.Revision != current.Revision || body.BaseRevision != current.BaseRevision {
		guestRoutineDraftError(w, pipeline.ErrDraftConflict)
		return
	}
	// Retain server-owned draft metadata. The caller may replace only these
	// content fields; its identity cannot choose an author or execution origin.
	document["name"], _ = json.Marshal(body.Name)
	document["description"], _ = json.Marshal(*body.Description)
	document["definition"] = body.Definition
	stripRoutineJSONFields(document, "save_token", "skip_test_gate", "skip_governance_gate", "last_test_run_at", "last_test_run_passed", "source_run_id", "source_agent_id", "chat_origin_id")
	encoded, err := encodeDraftDocument(document)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	current.Document, current.UpdatedBy = encoded, user.ID
	saved, err := drafts.SaveDraftIn(req.Context(), tx, *current)
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	notify, err := r.appendGuestIssueAudit(req.Context(), tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryAuditEntityUpdated, ActorType: journal.ActorUser, ActorID: user.ID, Summary: "Guest edited a shared routine draft", Payload: map[string]any{"entity_type": "routine_draft", "routine_id": routineID, "draft_id": saved.ID, "revision": saved.Revision, "base_revision": saved.BaseRevision, "fields": []string{"name", "description", "definition"}}})
	if err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		guestRoutineDraftError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	body.DraftID, body.Revision = saved.ID, saved.Revision
	writeJSON(w, 200, guestRoutineDraft{guestRoutineDraftContent: body, CanEdit: true})
}
