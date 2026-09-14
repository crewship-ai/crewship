package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type internalDraftRequest struct {
	WorkspaceID    string         `json:"workspace_id"`
	Slug           string         `json:"slug"`
	AuthorCrewID   string         `json:"author_crew_id"`
	AuthorAgentID  string         `json:"author_agent_id"`
	TargetCrewSlug string         `json:"target_crew_slug,omitempty"`
	Draft          pipeline.Draft `json:"draft"`
}

// Agent edits use the same draft CAS as the user editor, scoped to the crew
// authenticated by the sidecar. They never publish or mint a publication proof.
func (h *PipelineHandler) internalDraft(w http.ResponseWriter, r *http.Request, save bool) {
	var in internalDraftRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&in); err != nil || in.WorkspaceID == "" || in.Slug == "" || strings.ContainsAny(in.Slug, "/?#\\") {
		replyError(w, http.StatusBadRequest, "workspace_id and slug required")
		return
	}
	if !assertInternalTokenWorkspace(w, r, in.WorkspaceID) || !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &in.AuthorCrewID) {
		return
	}
	callerCrew, callerAgent := in.AuthorCrewID, in.AuthorAgentID
	if !h.assertAuthorAgentInCrew(w, r, in.WorkspaceID, callerCrew, callerAgent) {
		return
	}
	if !resolveDelegatedAuthorCrew(w, r, h.db, h.logger, in.WorkspaceID, in.TargetCrewSlug, &in.AuthorCrewID) {
		return
	}
	if in.TargetCrewSlug != "" {
		in.AuthorAgentID = ""
	}
	if in.AuthorCrewID == "" {
		replyError(w, http.StatusForbidden, "draft author crew required")
		return
	}
	current, err := h.store.GetDraft(r.Context(), in.WorkspaceID, in.Slug)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Could not load draft")
		return
	}
	// Do not expose another crew's unpublished work, even to an agent that can
	// list the published workspace catalog. Reassignment requires the user UI.
	var document map[string]json.RawMessage
	if err = json.Unmarshal(current.Document, &document); err != nil {
		replyError(w, 500, "Could not read draft")
		return
	}
	if len(document) > 0 {
		var owner string
		json.Unmarshal(document["author_crew_id"], &owner)
		if owner != in.AuthorCrewID {
			replyError(w, http.StatusForbidden, "draft belongs to another crew")
			return
		}
	}
	if p, err := h.store.GetBySlug(r.Context(), in.WorkspaceID, in.Slug); err == nil {
		if p.AuthorCrewID != in.AuthorCrewID {
			replyError(w, http.StatusForbidden, "published recipe belongs to another crew")
			return
		}
	} else if !errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, 500, "Could not check published recipe")
		return
	}
	if save {
		d := in.Draft
		// The ownership check above applies to this slug's current draft.
		// Agent saves cannot use another draft's ID to rename it past that check.
		if d.ID != "" && d.ID != current.ID {
			replyError(w, http.StatusConflict, pipeline.ErrDraftConflict.Error())
			return
		}
		if d.Slug != in.Slug || d.Revision < 0 {
			replyError(w, 400, "draft slug and revision required")
			return
		}
		var body userSaveRequest
		if err = json.Unmarshal(d.Document, &body); err != nil || body.Slug != in.Slug || !draftDefinitionObject(body.Definition) {
			replyError(w, 400, "draft document slug and definition required")
			return
		}
		// A save replaces the document; unmarshalling into the ownership-check
		// map above would retain keys deliberately removed by the author.
		document = nil
		if err = json.Unmarshal(d.Document, &document); err != nil || document == nil {
			replyError(w, 400, "draft document required")
			return
		}
		for _, key := range []string{"save_token", "skip_test_gate", "skip_governance_gate", "last_test_run_at", "last_test_run_passed"} {
			delete(document, key)
		}
		document["author_crew_id"], _ = json.Marshal(in.AuthorCrewID)
		document["author_agent_id"], _ = json.Marshal(in.AuthorAgentID)
		if d.Document, err = encodeDraftDocument(document); err != nil {
			replyError(w, 500, "Could not prepare draft")
			return
		}
		d.WorkspaceID = in.WorkspaceID
		d.UpdatedBy = "crew:" + callerCrew
		if callerAgent != "" {
			d.UpdatedBy = "agent:" + callerAgent
		}
		current, err = h.store.SaveDraft(r.Context(), d)
		if errors.Is(err, pipeline.ErrDraftConflict) {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		if err != nil {
			h.logger.Error("save agent draft", "error", err)
			replyError(w, 500, "Could not save draft")
			return
		}
	}
	// A stable draft id lets the UI reject an old link after discard/recreation.
	editorURL := "/routines?" + url.Values{"draft": {in.Slug}, "draft_id": {current.ID}, "workspace": {in.WorkspaceID}}.Encode()
	writeJSON(w, http.StatusOK, map[string]any{"draft": current, "editor_url": editorURL, "published": false})
}
func (h *PipelineHandler) InternalGetDraft(w http.ResponseWriter, r *http.Request) {
	h.internalDraft(w, r, false)
}
func (h *PipelineHandler) InternalSaveDraft(w http.ResponseWriter, r *http.Request) {
	h.internalDraft(w, r, true)
}
