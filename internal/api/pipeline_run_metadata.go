package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// UpdateRunMetadata applies set/increment/append mutations to a run's
// metadata scratchpad (trigger.dev metadata.* parity). Lets an agent or
// external caller thread state through a run mid-flight; readable from
// later steps as {{ run.metadata.x }}.
// PATCH /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/metadata
func (h *PipelineHandler) UpdateRunMetadata(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
		return
	}
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run store not wired")
		return
	}
	var ops pipeline.MetadataOps
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&ops); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(ops.Set) == 0 && len(ops.Increment) == 0 && len(ops.Append) == 0 {
		replyError(w, http.StatusBadRequest, "no metadata ops (pass set, increment, and/or append)")
		return
	}
	merged, err := h.runStore.UpdateMetadata(r.Context(), workspaceID, runID, ops)
	if errors.Is(err, pipeline.ErrRunNotFoundInStore) {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.logger.Error("update run metadata", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "update metadata")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metadata": merged})
}

// GetRunTree returns a run and its descendants (call_pipeline / deferred
// / replay parentage) as a flat, parent-linked list.
// GET /api/v1/workspaces/{workspaceId}/pipeline-runs/{runId}/tree
func (h *PipelineHandler) GetRunTree(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
		return
	}
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run store not wired")
		return
	}
	nodes, err := h.runStore.RunTree(r.Context(), workspaceID, runID)
	if err != nil {
		h.logger.Error("run tree", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "load run tree")
		return
	}
	if len(nodes) == 0 {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}
	type node struct {
		ID           string  `json:"id"`
		ParentID     string  `json:"parent_id,omitempty"`
		PipelineSlug string  `json:"pipeline_slug"`
		Status       string  `json:"status"`
		TriggeredVia string  `json:"triggered_via"`
		CostUSD      float64 `json:"cost_usd"`
	}
	out := make([]node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, node{n.ID, n.ParentID, n.PipelineSlug, n.Status, n.TriggeredVia, n.CostUSD})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}
