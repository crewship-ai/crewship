package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// CancelRun POST /workspaces/{wsId}/pipelines/runs/{runId}/cancel
//
// Pre-empts an in-flight pipeline run by triggering its context.
// The run loop checks ctx.Err() between steps and propagates the
// cancellation into the AgentRunner, which kills the underlying CLI
// process.
//
// Idempotent: cancelling an already-cancelled run is a no-op (200
// with same response). Cancelling a finished run returns 404 because
// the registry only tracks live runs.
func (h *PipelineHandler) CancelRun(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "run registry not wired",
			"hint":  "the cancel API requires the in-memory registry; tests / dev builds may skip this",
		})
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "runId required"})
		return
	}

	// Authorization scope — only cancel runs in this workspace.
	// Without the scope check, a workspace_a user could cancel
	// workspace_b's runs by guessing the runID. The registry's
	// IsCancelRequested doesn't expose workspace metadata, so we
	// scan Active() for a matching id.
	found := false
	for _, info := range h.runs.Active(workspaceID) {
		if info.RunID == runID {
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "run not found in this workspace (already finished or not started here)",
		})
		return
	}

	if err := h.runs.Cancel(runID); err != nil {
		if errors.Is(err, pipeline.ErrRunNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		h.logger.Warn("cancel pipeline run", "error", err, "run_id", runID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to cancel run"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":              runID,
		"cancel_requested":    true,
		"cancel_requested_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// ListActiveRuns GET /workspaces/{wsId}/pipelines/runs/active
//
// Returns the current in-flight run set scoped to this workspace.
// Used by the inbox UI / dashboard to show "what is running right
// now" with cancel buttons. Single-instance scope: a multi-replica
// deployment would only see runs on the queried replica until we
// add a leader-elected shared registry.
func (h *PipelineHandler) ListActiveRuns(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		// Empty list when the registry isn't wired — the UI should
		// degrade gracefully rather than show an error banner.
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	out := h.runs.Active(workspaceID)
	resp := make([]map[string]any, 0, len(out))
	for _, info := range out {
		resp = append(resp, map[string]any{
			"run_id":           info.RunID,
			"workspace_id":     info.WorkspaceID,
			"pipeline_id":      info.PipelineID,
			"pipeline_slug":    info.PipelineSlug,
			"concurrency_key":  info.ConcurrencyKey,
			"started_at":       info.StartedAt.UTC().Format(time.RFC3339Nano),
			"cancel_requested": info.CancelRequested,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}
