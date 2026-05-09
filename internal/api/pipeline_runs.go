package api

import (
	"database/sql"
	"encoding/json"
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

// GetRun GET /workspaces/{wsId}/pipeline-runs/{runId}
//
// Top-level /pipeline-runs/ instead of /pipelines/runs/ to avoid a
// net/http ServeMux pattern conflict with /pipelines/{slug}/runs.
//
// Returns the persisted state of a single pipeline run — status,
// current step, accumulated step outputs, error info. Used by the
// inbox waitpoint detail panel to render "where did it pause" with
// real data from pipeline_runs (the projection table v83 introduced).
//
// step_outputs_json is parsed server-side into an object so the UI
// doesn't have to JSON.parse twice; the frontend renders one panel
// per (step_id, output) pair.
func (h *PipelineHandler) GetRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "runId required"})
		return
	}

	var (
		id, wsID, pipelineID, pipelineSlug, status, mode string
		currentStepID, stepOutputsJSON, output           sql.NullString
		startedAt                                        string
		endedAt, errorMessage, failedAtStep              sql.NullString
		costUSD                                          float64
		durationMs                                       int64
		triggeredVia, triggeredByID, idempotencyKey      sql.NullString
		inputsJSON                                       string
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, workspace_id, pipeline_id, pipeline_slug, status, mode,
		       current_step_id, step_outputs_json, output, started_at,
		       ended_at, error_message, failed_at_step,
		       cost_usd, duration_ms,
		       triggered_via, triggered_by_id, idempotency_key, inputs_json
		FROM pipeline_runs
		WHERE id = ? AND workspace_id = ?`,
		runID, workspaceID,
	).Scan(
		&id, &wsID, &pipelineID, &pipelineSlug, &status, &mode,
		&currentStepID, &stepOutputsJSON, &output, &startedAt,
		&endedAt, &errorMessage, &failedAtStep,
		&costUSD, &durationMs,
		&triggeredVia, &triggeredByID, &idempotencyKey, &inputsJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
		return
	}
	if err != nil {
		h.logger.Error("get pipeline run", "error", err, "run_id", runID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load run"})
		return
	}

	// Parse step_outputs_json into a map so the UI can iterate steps
	// without a second JSON.parse. Default to empty object on parse
	// failure rather than failing the whole call — the rest of the
	// metadata is still useful.
	var stepOutputs map[string]interface{}
	if stepOutputsJSON.Valid && stepOutputsJSON.String != "" {
		_ = json.Unmarshal([]byte(stepOutputsJSON.String), &stepOutputs)
	}
	var inputs map[string]interface{}
	if inputsJSON != "" {
		_ = json.Unmarshal([]byte(inputsJSON), &inputs)
	}

	resp := map[string]interface{}{
		"id":              id,
		"workspace_id":    wsID,
		"pipeline_id":     pipelineID,
		"pipeline_slug":   pipelineSlug,
		"status":          status,
		"mode":            mode,
		"current_step_id": currentStepID.String,
		"step_outputs":    stepOutputs,
		"output":          output.String,
		"started_at":      startedAt,
		"ended_at":        endedAt.String,
		"error_message":   errorMessage.String,
		"failed_at_step":  failedAtStep.String,
		"cost_usd":        costUSD,
		"duration_ms":     durationMs,
		"triggered_via":   triggeredVia.String,
		"triggered_by_id": triggeredByID.String,
		"idempotency_key": idempotencyKey.String,
		"inputs":          inputs,
	}
	writeJSON(w, http.StatusOK, resp)
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
