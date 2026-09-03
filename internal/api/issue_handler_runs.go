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
//
// ID is the assignment id — the row's identity for this list. RunID and
// TraceID are the journal run the assignment produced (one and the same
// value: trace_id == run id, see assignments_run.go), resolved through the
// run.started entry that names this assignment in its payload. They are what
// a client needs to open the run (`/activity?run=`) or its journal
// (`/journal?trace_id=`); an assignment that never reached a run — refused
// before dispatch, or predating the journal — carries neither. AgentID and
// AgentSlug let the same client link the agent (`/crews?agent=<slug>`)
// instead of printing a name it cannot follow.
type issueRunDTO struct {
	ID            string `json:"id"`
	RunID         string `json:"run_id,omitempty"`
	TraceID       string `json:"trace_id,omitempty"`
	Status        string `json:"status"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentSlug     string `json:"agent_slug,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	Task          string `json:"task,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	EndedAt       string `json:"ended_at,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	ResultSummary string `json:"result_summary,omitempty"`
	ErrorMessage  string `json:"error_message,omitempty"`
}

// parseRunTime accepts the timestamp shapes the engine + SQLite defaults
// emit: RFC3339[Nano] from Go, "2006-01-02 15:04:05" from datetime('now'),
// and the fractional "2006-01-02 15:04:05.999" written by
// datetime('now','subsec') — the shape MarkInterrupted/RecoverInterrupted
// stamp into ended_at (internal/pipeline/runs.go). The plain-second layout
// already covers the fractional case because time.Parse tolerates a
// trailing fraction the layout doesn't name; the explicit subsec layout is
// kept as a defensive pin (guarded by TestRunFiles_InterruptedRun_SubsecEndedAt)
// so a future switch to a stricter parser can't silently widen the run→files
// window to now() for interrupted runs (#891).
func parseRunTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
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
// Newest-first, paged the S1 way (`?limit=&offset=`, total in
// X-Total-Count) so every run stays reachable: the list used to stop at
// 100 with nothing saying so.
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
		internalError(w, r, h.logger, "issue runs: resolve mission", err)
		return
	}

	limit, offset := parsePagination(r, 100, 500)

	var total int
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*)
		FROM mission_tasks mt
		JOIN assignments a ON a.id = mt.assignment_id
		WHERE mt.mission_id = ? AND a.workspace_id = ?`, missionID, wsID).Scan(&total); err != nil {
		internalError(w, r, h.logger, "issue runs: count", err)
		return
	}

	// The run id comes from the journal: assignments_run.go emits one
	// run.started entry per dispatched assignment with trace_id = run id,
	// mission_id = this mission and payload.assignment_id = the row. The
	// correlated subquery walks idx_journal_mission_ts, so it is bounded by
	// the issue's own entries, not the workspace's.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.status, a.started_at, a.finished_at, a.result_summary,
		       a.error_message, a.task,
		       COALESCE(ag.name, ''), COALESCE(ag.id, ''), COALESCE(ag.slug, ''),
		       (SELECT je.trace_id FROM journal_entries je
		         WHERE je.mission_id = mt.mission_id
		           AND je.entry_type = 'run.started'
		           AND json_extract(je.payload, '$.assignment_id') = a.id
		         ORDER BY je.ts DESC LIMIT 1)
		FROM mission_tasks mt
		JOIN assignments a ON a.id = mt.assignment_id
		LEFT JOIN agents ag ON ag.id = a.assigned_to_id
		WHERE mt.mission_id = ? AND a.workspace_id = ?
		ORDER BY COALESCE(a.started_at, a.created_at) DESC
		LIMIT ? OFFSET ?`, missionID, wsID, limit, offset)
	if err != nil {
		internalError(w, r, h.logger, "issue runs: query", err)
		return
	}
	defer rows.Close()

	out := []issueRunDTO{}
	for rows.Next() {
		var (
			dto                                            issueRunDTO
			started, finished, result, errMsg, task, runID sql.NullString
		)
		if err := rows.Scan(&dto.ID, &dto.Status, &started, &finished, &result,
			&errMsg, &task, &dto.AgentName, &dto.AgentID, &dto.AgentSlug, &runID); err != nil {
			internalError(w, r, h.logger, "issue runs: scan", err)
			return
		}
		dto.RunID = runID.String
		dto.TraceID = runID.String
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
		internalError(w, r, h.logger, "issue runs: rows", err)
		return
	}

	writeListMeta(w, total, limit, offset)
	writeJSON(w, http.StatusOK, out)
}
