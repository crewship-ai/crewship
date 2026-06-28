package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Routine-definition tags (v123) for cross-crew discovery. Distinct from
// run tags (per-run labels). PUT adds, DELETE removes, and the routine
// list accepts ?tag= to browse by tag.

// AddPipelineTags tags a routine.
// PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags
func (h *PipelineHandler) AddPipelineTags(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Tags) == 0 {
		replyError(w, http.StatusBadRequest, "tags required")
		return
	}
	store := pipeline.NewPipelineTagStore(h.db)
	if err := store.Add(r.Context(), workspaceID, p.ID, body.Tags); err != nil {
		if errors.Is(err, pipeline.ErrTooManyTags) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		replyError(w, http.StatusInternalServerError, "add tags")
		return
	}
	tags, _ := store.TagsFor(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

// RemovePipelineTag untags a routine.
// DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/tags/{tag}
func (h *PipelineHandler) RemovePipelineTag(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	tag := r.PathValue("tag")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	store := pipeline.NewPipelineTagStore(h.db)
	if err := store.Remove(r.Context(), p.ID, tag); err != nil {
		replyError(w, http.StatusInternalServerError, "remove tag")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
