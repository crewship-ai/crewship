package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	// Audit M2-promoted: cancelling another user's run is a manage-
	// tier action -- otherwise a MEMBER can stop production
	// pipelines that an OWNER/ADMIN kicked off.
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
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
			replyError(w, http.StatusNotFound, "run not found")
			return
		}
		h.logger.Warn("cancel pipeline run", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "failed to cancel run")
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
		replyError(w, http.StatusBadRequest, "runId required")
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
		pipelineName, issueIdentifier                    sql.NullString
		metadataJSON, replayOf                           sql.NullString
		isReplay                                         int64
	)
	// Same LEFT JOIN as ListWorkspaceRuns so /pipeline-runs/{id}
	// returns the human pipeline name + issue identifier without
	// forcing the FE to do a second fetch. The trace canvas
	// (/activity) needs both for its trigger node + toolbar label.
	//
	// `p.deleted_at IS NULL` matches the soft-delete contract used by
	// every other pipelines query (v78 migration). Without this, a
	// run from a deleted pipeline would still surface the deleted
	// pipeline's name to anyone who can guess the run id.
	err := h.db.QueryRowContext(r.Context(), `
		SELECT r.id, r.workspace_id, r.pipeline_id, r.pipeline_slug, r.status, r.mode,
		       r.current_step_id, r.step_outputs_json, r.output, r.started_at,
		       r.ended_at, r.error_message, r.failed_at_step,
		       r.cost_usd, r.duration_ms,
		       r.triggered_via, r.triggered_by_id, r.idempotency_key, r.inputs_json,
		       r.metadata_json, r.is_replay, r.replay_of,
		       p.name, m.identifier
		FROM pipeline_runs r
		LEFT JOIN pipelines p ON r.pipeline_id = p.id
		                     AND p.workspace_id = r.workspace_id
		                     AND p.deleted_at IS NULL
		LEFT JOIN missions m ON r.triggered_via = 'issue'
		                    AND m.identifier = r.triggered_by_id
		                    AND m.workspace_id = r.workspace_id
		WHERE r.id = ? AND r.workspace_id = ?`,
		runID, workspaceID,
	).Scan(
		&id, &wsID, &pipelineID, &pipelineSlug, &status, &mode,
		&currentStepID, &stepOutputsJSON, &output, &startedAt,
		&endedAt, &errorMessage, &failedAtStep,
		&costUSD, &durationMs,
		&triggeredVia, &triggeredByID, &idempotencyKey, &inputsJSON,
		&metadataJSON, &isReplay, &replayOf,
		&pipelineName, &issueIdentifier,
	)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.logger.Error("get pipeline run", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "load run")
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
	var metadata map[string]interface{}
	if metadataJSON.Valid && metadataJSON.String != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &metadata)
	}
	// Tags live in the run_tags join table; best-effort fetch so a tag
	// query failure doesn't sink the whole run detail.
	var tags []string
	if h.runStore != nil {
		if t, err := h.runStore.TagsFor(r.Context(), runID); err == nil {
			tags = t
		}
	}

	resp := map[string]interface{}{
		"id":               id,
		"workspace_id":     wsID,
		"pipeline_id":      pipelineID,
		"pipeline_slug":    pipelineSlug,
		"pipeline_name":    pipelineName.String,
		"status":           status,
		"mode":             mode,
		"current_step_id":  currentStepID.String,
		"step_outputs":     stepOutputs,
		"output":           output.String,
		"started_at":       startedAt,
		"ended_at":         endedAt.String,
		"error_message":    errorMessage.String,
		"failed_at_step":   failedAtStep.String,
		"cost_usd":         costUSD,
		"duration_ms":      durationMs,
		"triggered_via":    triggeredVia.String,
		"triggered_by_id":  triggeredByID.String,
		"idempotency_key":  idempotencyKey.String,
		"inputs":           inputs,
		"issue_identifier": issueIdentifier.String,
		"metadata":         metadata,
		"is_replay":        isReplay != 0,
		"replay_of":        replayOf.String,
		"tags":             tags,
	}
	writeJSON(w, http.StatusOK, resp)
}

// runLogEntry is the wire shape for one line in a run's console. Mirrors
// the loose {ts, level, message} shape the dashboard log renderers already
// consume (exec-tab / logs-tab), so the same component works for agent
// logs and run logs without branching.
type runLogEntry struct {
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

// RunLogs GET /workspaces/{wsId}/pipeline-runs/{runId}/logs
//
// The exec console for a single run. Reads journal_entries scoped to the
// run's trace_id (run id == trace_id) and projects each entry to a log
// line. Powers the dock's Logs tab on /activity (the selected run) and
// /routines (the routine's latest run, resolved by the client first).
//
// Workspace-scoped via journal.Query.WorkspaceID, and the run is confirmed
// to exist in this workspace first so a foreign / unknown run id surfaces
// as 404 rather than an empty log (consistent with GetRun's existence
// masking). Returns oldest-first so the console reads top-to-bottom.
func (h *PipelineHandler) RunLogs(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
		return
	}
	// Run logs are journal-backed and can carry prompts / partial responses
	// — gate on read like AgentLogs / the git-diff endpoints, and fail
	// closed on an empty/unmapped role.
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	// Existence + workspace scope check — masks cross-tenant lookups as
	// 404 the same way GetRun does.
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM pipeline_runs WHERE id = ? AND workspace_id = ?`, runID, workspaceID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		h.logger.Error("run logs: existence check", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "load run")
		return
	}

	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, perr := parseSmallInt(v); perr == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	// Pipeline runs tag their journal entries with the run id in the
	// payload (payload.run_id) — NOT the trace_id column (trace_id carries
	// the OTel/mission trace). Agent-driven runs use trace_id instead. Match
	// either so the console works for both. `run_id` is the VIRTUAL generated
	// column (v120) over payload.run_id, indexed via idx_journal_ws_run, so
	// both OR arms are index-unionable instead of full-scanning the journal.
	// ORDER BY ts ASC → oldest-first.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ts, severity, entry_type, summary
		FROM journal_entries
		WHERE workspace_id = ?
		  AND (trace_id = ? OR run_id = ?)
		ORDER BY ts ASC
		LIMIT ?`, workspaceID, runID, runID, limit)
	if err != nil {
		h.logger.Error("run logs: query", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "load logs")
		return
	}
	defer rows.Close()

	out := make([]runLogEntry, 0, limit)
	for rows.Next() {
		var ts, severity, entryType, summary string
		if err := rows.Scan(&ts, &severity, &entryType, &summary); err != nil {
			h.logger.Error("run logs: scan", "error", err, "run_id", runID)
			replyError(w, http.StatusInternalServerError, "load logs")
			return
		}
		if severity == "" {
			severity = "info"
		}
		out = append(out, runLogEntry{
			TS:      ts,
			Level:   severity,
			Message: summary,
			Type:    entryType,
		})
	}
	if err := rows.Err(); err != nil {
		// Don't present a half-streamed log as complete.
		h.logger.Error("run logs: rows", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "load logs")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListWorkspaceRuns GET /workspaces/{wsId}/pipeline-runs
//
// Workspace-scoped run feed for the /activity Runs sub-tab. Returns
// recent runs across every pipeline with enrichment (pipeline_name,
// issue_identifier when triggered_via=issue) so the UI can render
// the source-pill chip without a second fetch.
//
// Filters:
//
//	?status=running|completed|failed|...|active   (active = running+queued+paused)
//	?since=<RFC3339>                              created_at lower bound
//	?limit=50                                     hard cap 200
//
// Sorted by started_at DESC — newest first matches the user's mental
// model ("what just happened / is happening now"). Older runs paginate
// with ?since= rather than offset because cron-heavy workspaces churn
// fast enough that page numbers drift between requests.
func (h *PipelineHandler) ListWorkspaceRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	q := r.URL.Query()

	limit := 50
	if v := q.Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	args := []interface{}{workspaceID}
	where := []string{"r.workspace_id = ?"}
	switch q.Get("status") {
	case "active":
		// "active" is the dashboard shortcut for "still doing
		// something" — running + queued + paused (paused covers
		// runs sitting on a wait step waiting on an inbox approval).
		where = append(where, `r.status IN ('running', 'queued', 'paused')`)
	case "":
		// no filter
	default:
		where = append(where, "r.status = ?")
		args = append(args, q.Get("status"))
	}
	if since := q.Get("since"); since != "" {
		where = append(where, "r.created_at >= ?")
		args = append(args, since)
	}

	// LEFT JOIN pipelines for the human name + LEFT JOIN missions on
	// triggered_by_id (when triggered_via='issue') for the issue
	// identifier. Both joins are workspace-scoped so a stale ID can't
	// leak from another tenant. `p.deleted_at IS NULL` matches every
	// other pipelines query (v78) so a soft-deleted pipeline doesn't
	// resurface its name in run lists.
	query := `
		SELECT r.id, r.pipeline_id, r.pipeline_slug, p.name,
		       r.status, r.mode, r.started_at, r.ended_at,
		       r.current_step_id, r.step_outputs_json,
		       r.cost_usd, r.duration_ms,
		       r.triggered_via, r.triggered_by_id,
		       r.invoking_crew_id, r.invoking_agent_id, r.invoking_user_id,
		       r.error_message, r.failed_at_step,
		       m.identifier
		FROM pipeline_runs r
		LEFT JOIN pipelines p ON r.pipeline_id = p.id
		                     AND p.workspace_id = r.workspace_id
		                     AND p.deleted_at IS NULL
		LEFT JOIN missions m ON r.triggered_via = 'issue'
		                    AND m.identifier = r.triggered_by_id
		                    AND m.workspace_id = r.workspace_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY r.started_at DESC
		LIMIT ?`
	args = append(args, limit)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list pipeline runs", "error", err)
		replyError(w, http.StatusInternalServerError, "list runs")
		return
	}
	defer rows.Close()

	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var (
			id, pipelineID, pipelineSlug, status, mode, startedAt string
			pipelineName, currentStepID, stepOutputsJSON          sql.NullString
			endedAt                                               sql.NullString
			costUSD                                               float64
			durationMs                                            int64
			triggeredVia, triggeredByID                           sql.NullString
			invokingCrewID, invokingAgentID, invokingUserID       sql.NullString
			errorMessage, failedAtStep                            sql.NullString
			issueIdentifier                                       sql.NullString
		)
		if err := rows.Scan(
			&id, &pipelineID, &pipelineSlug, &pipelineName,
			&status, &mode, &startedAt, &endedAt,
			&currentStepID, &stepOutputsJSON,
			&costUSD, &durationMs,
			&triggeredVia, &triggeredByID,
			&invokingCrewID, &invokingAgentID, &invokingUserID,
			&errorMessage, &failedAtStep,
			&issueIdentifier,
		); err != nil {
			h.logger.Warn("scan pipeline run", "error", err)
			continue
		}
		var stepOutputs map[string]interface{}
		if stepOutputsJSON.Valid && stepOutputsJSON.String != "" {
			_ = json.Unmarshal([]byte(stepOutputsJSON.String), &stepOutputs)
		}
		out = append(out, map[string]interface{}{
			"id":                id,
			"pipeline_id":       pipelineID,
			"pipeline_slug":     pipelineSlug,
			"pipeline_name":     pipelineName.String,
			"status":            status,
			"mode":              mode,
			"started_at":        startedAt,
			"ended_at":          endedAt.String,
			"current_step_id":   currentStepID.String,
			"step_outputs":      stepOutputs,
			"cost_usd":          costUSD,
			"duration_ms":       durationMs,
			"triggered_via":     triggeredVia.String,
			"triggered_by_id":   triggeredByID.String,
			"invoking_crew_id":  invokingCrewID.String,
			"invoking_agent_id": invokingAgentID.String,
			"invoking_user_id":  invokingUserID.String,
			"error_message":     errorMessage.String,
			"failed_at_step":    failedAtStep.String,
			"issue_identifier":  issueIdentifier.String,
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rows":  out,
		"count": len(out),
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
