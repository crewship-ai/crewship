package api

import (
	"database/sql"
	"errors"
	"net/http"
)

// issueRunDTO is the wire shape for one run worked on an issue. Kept
// column-compatible with the routine run-records DTO (pipelines_exec.go) so
// the dashboard's single Runs table renders both without branching.
type issueRunDTO struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Mode         string  `json:"mode,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	EndedAt      string  `json:"ended_at,omitempty"`
	DurationMs   int64   `json:"duration_ms"`
	CostUSD      float64 `json:"cost_usd"`
	TriggeredVia string  `json:"triggered_via"`
	ErrorMessage string  `json:"error_message,omitempty"`
}

// ListRuns GET /api/v1/crews/{crewId}/issues/{identifier}/runs
//
// Lists the pipeline runs triggered by this issue. Issue-triggered runs
// carry triggered_via='issue' + triggered_by_id=<identifier> (the same
// linkage GetRun's LEFT JOIN uses to resolve a run's issue), so this is a
// direct, indexed read of pipeline_runs rather than a journal scan.
//
// The mission is resolved first so an unknown / foreign issue surfaces as
// 404 instead of an empty list. Newest-first — "what just ran".
func (h *IssueHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	if _, err := h.resolveMissionID(r.Context(), ident, crewID, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("issue runs: resolve mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, status, mode, started_at, ended_at, duration_ms, cost_usd,
		       triggered_via, error_message
		FROM pipeline_runs
		WHERE workspace_id = ? AND triggered_via = 'issue' AND triggered_by_id = ?
		ORDER BY started_at DESC
		LIMIT 100`, wsID, ident)
	if err != nil {
		h.logger.Error("issue runs: query", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := []issueRunDTO{}
	for rows.Next() {
		var (
			dto                              issueRunDTO
			mode, startedAt, endedAt, errMsg sql.NullString
		)
		if err := rows.Scan(&dto.ID, &dto.Status, &mode, &startedAt, &endedAt,
			&dto.DurationMs, &dto.CostUSD, &dto.TriggeredVia, &errMsg); err != nil {
			h.logger.Error("issue runs: scan", "error", err)
			continue
		}
		dto.Mode = mode.String
		dto.StartedAt = startedAt.String
		dto.EndedAt = endedAt.String
		// Same hard truncation as the routine run list — error_message is
		// verbatim executor output and could carry stack traces / paths.
		dto.ErrorMessage = truncateErrorForList(errMsg.String)
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("issue runs: rows", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, out)
}
