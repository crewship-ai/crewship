package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
)

// RunExecutions is workspace-scoped and paginated. Large immutable outputs are
// only selected on demand; polling the tree never downloads every transcript.
func (h *PipelineHandler) RunExecutions(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "read") {
		replyError(w, 403, "Forbidden")
		return
	}
	ws, runID := WorkspaceIDFromContext(r.Context()), r.PathValue("runId")
	var exists int
	err := h.db.QueryRowContext(r.Context(), `SELECT 1 FROM pipeline_runs WHERE id=? AND workspace_id=?`, runID, ws).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "run not found")
		return
	}
	if err != nil {
		replyError(w, 500, "load run")
		return
	}
	if executionID := r.URL.Query().Get("execution_id"); executionID != "" {
		var output string
		err := h.db.QueryRowContext(r.Context(), `SELECT output FROM pipeline_step_executions WHERE id=? AND run_id=?`, executionID, runID).Scan(&output)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "execution not found")
			return
		}
		if err != nil {
			replyError(w, 500, "load execution output")
			return
		}
		writeJSON(w, 200, pipelineRunStepExecutionOutput{ID: executionID, Output: output})
		return
	}
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			replyError(w, 400, "invalid cursor")
			return
		}
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT e.rowid,e.id,e.parent_execution_id,e.step_id,e.execution_path,e.attempt,e.kind,
        CASE WHEN e.status='running' AND r.status IN ('cancelled','interrupted','failed','completed') THEN 'interrupted' ELSE e.status END,
        e.agent_slug,e.model,e.started_at,e.ended_at,e.error,length(e.output)
        FROM pipeline_step_executions e JOIN pipeline_runs r ON r.id=e.run_id WHERE e.run_id=? AND e.rowid>? ORDER BY e.rowid LIMIT 101`, runID, after)
	if err != nil {
		replyError(w, 500, "load step executions")
		return
	}
	defer rows.Close()
	records := make([]pipelineRunStepExecution, 0)
	var next int64
	more := false
	for rows.Next() {
		var seq int64
		var id, step, path, kind, status, agent, model, started, reason string
		var parent, ended sql.NullString
		var attempt, size int
		if err := rows.Scan(&seq, &id, &parent, &step, &path, &attempt, &kind, &status, &agent, &model, &started, &ended, &reason, &size); err != nil {
			replyError(w, 500, "read step executions")
			return
		}
		if len(records) == 100 {
			more = true
			break
		}
		next = seq
		records = append(records, pipelineRunStepExecution{
			ID: id, ParentExecutionID: parent.String, StepID: step, ExecutionPath: path,
			Attempt: attempt, Kind: kind, Status: status, AgentSlug: agent, Model: model,
			StartedAt: started, EndedAt: ended.String, Error: reason, OutputBytes: size,
		})
	}
	if rows.Err() != nil {
		replyError(w, 500, "read step executions")
		return
	}
	var cursor *string
	if more {
		encoded := strconv.FormatInt(next, 10)
		cursor = &encoded
	}
	writeJSON(w, 200, pipelineRunStepExecutionList{Rows: records, NextCursor: cursor})
}
