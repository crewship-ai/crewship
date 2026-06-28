package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Per-step prompt/model override layer (v121). Lets an operator tweak a
// single step's prompt or tier without bumping the routine version — the
// override is applied at run start over the versioned DSL.

// SetStepOverride upserts a prompt/model override for one step.
// PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override
func (h *PipelineHandler) SetStepOverride(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	stepID := r.PathValue("stepId")
	if stepID == "" {
		replyError(w, http.StatusBadRequest, "stepId required")
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	// Validate the step exists in the current definition so an operator
	// can't pin an override to a typo'd step id that never runs.
	dsl, perr := pipeline.Parse([]byte(p.DefinitionJSON))
	if perr != nil {
		replyError(w, http.StatusInternalServerError, "parse pipeline")
		return
	}
	found := false
	for _, s := range dsl.Steps {
		if s.ID == stepID {
			found = true
			break
		}
	}
	if !found {
		replyError(w, http.StatusNotFound, "step not found in routine definition")
		return
	}

	var body struct {
		Prompt        string `json:"prompt"`
		ModelOverride string `json:"model_override"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if body.Prompt == "" && body.ModelOverride == "" {
		replyError(w, http.StatusBadRequest, "override requires prompt and/or model_override")
		return
	}
	store := pipeline.NewStepOverrideStore(h.db)
	if err := store.Set(r.Context(), workspaceID, p.ID, stepID, body.Prompt, body.ModelOverride); err != nil {
		h.logger.Error("set step override", "error", err)
		replyError(w, http.StatusInternalServerError, "save override")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "step_id": stepID})
}

// DeleteStepOverride removes a step's override (reverts to authored).
// DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/steps/{stepId}/override
func (h *PipelineHandler) DeleteStepOverride(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	stepID := r.PathValue("stepId")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	store := pipeline.NewStepOverrideStore(h.db)
	if err := store.Delete(r.Context(), p.ID, stepID); err != nil {
		h.logger.Error("delete step override", "error", err)
		replyError(w, http.StatusInternalServerError, "delete override")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "step_id": stepID})
}

// ListStepOverrides returns all overrides for a routine.
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/overrides
func (h *PipelineHandler) ListStepOverrides(w http.ResponseWriter, r *http.Request) {
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
	store := pipeline.NewStepOverrideStore(h.db)
	ov, err := store.OverridesFor(r.Context(), p.ID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load overrides")
		return
	}
	type row struct {
		StepID        string `json:"step_id"`
		Prompt        string `json:"prompt,omitempty"`
		ModelOverride string `json:"model_override,omitempty"`
	}
	out := make([]row, 0, len(ov))
	for stepID, o := range ov {
		out = append(out, row{StepID: stepID, Prompt: o.Prompt, ModelOverride: o.ModelOverride})
	}
	writeJSON(w, http.StatusOK, map[string]any{"overrides": out})
}
