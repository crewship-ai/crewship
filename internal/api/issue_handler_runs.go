package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// issueRunDTO is one execution of an issue's work — an agent assignment
// (mission task run). Issues run on the mission engine, not pipelines, so
// the rows here come from `assignments` joined to the mission via
// `mission_tasks`, not from pipeline_runs.
type issueRunDTO struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	AgentName     string `json:"agent_name,omitempty"`
	Task          string `json:"task,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	EndedAt       string `json:"ended_at,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	ResultSummary string `json:"result_summary,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// parseRunTime accepts the timestamp shapes the engine + SQLite defaults
// emit (RFC3339[Nano] from Go, "2006-01-02 15:04:05" from datetime('now')).
func parseRunTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ListRuns GET /api/v1/crews/{crewId}/issues/{identifier}/runs
//
// Lists the agent task-runs for an issue. Each mission task links to an
// `assignments` row (mission_tasks.assignment_id) carrying the execution
// status, timing, result, and error — the real "what ran" for an issue.
// Newest-first.
func (h *IssueHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("issue runs: resolve mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.status, a.started_at, a.finished_at, a.result_summary,
		       a.error_message, a.task, COALESCE(ag.name, '')
		FROM mission_tasks mt
		JOIN assignments a ON a.id = mt.assignment_id
		LEFT JOIN agents ag ON ag.id = a.assigned_to_id
		WHERE mt.mission_id = ? AND a.workspace_id = ?
		ORDER BY COALESCE(a.started_at, a.created_at) DESC
		LIMIT 100`, missionID, wsID)
	if err != nil {
		h.logger.Error("issue runs: query", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := []issueRunDTO{}
	for rows.Next() {
		var (
			dto                                     issueRunDTO
			started, finished, result, errMsg, task sql.NullString
		)
		if err := rows.Scan(&dto.ID, &dto.Status, &started, &finished, &result,
			&errMsg, &task, &dto.AgentName); err != nil {
			h.logger.Error("issue runs: scan", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		dto.StartedAt = started.String
		dto.EndedAt = finished.String
		dto.Task = task.String
		// result_summary is agent-authored prose; truncate hard like the
		// routine run list so a verbose summary can't bloat the row.
		dto.ResultSummary = truncateErrorForList(result.String)
		dto.ErrorMessage = truncateErrorForList(errMsg.String)
		if s, ok := parseRunTime(started.String); ok {
			if f, ok2 := parseRunTime(finished.String); ok2 && f.After(s) {
				dto.DurationMs = f.Sub(s).Milliseconds()
			}
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("issue runs: rows", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, out)
}
