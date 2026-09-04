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
	// MissionID is the issue (mission) this run is attributed to —
	// assignments.mission_id (#2256). Nullable: a legacy row written before
	// that column existed, and never touched by the backfill migration
	// (20260901180224), still shows up here (found via the mission_tasks
	// join below) but names no mission of its own.
	MissionID *string `json:"mission_id,omitempty"`
	// Source says WHY this run is attributed to the issue, not just that it
	// is (#2313): "task" — reached via mission_tasks.assignment_id, the
	// issue's own plan; "mention" — reached via
	// mission_comment_mentions.assignment_id, an @mention dispatch;
	// "delegation" — reached only via a.mission_id, a sub-agent's own
	// /assign call mid-mission. Always one of the three; never empty.
	Source string `json:"source,omitempty"`
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
		internalError(w, r, h.logger, "issue runs: resolve mission", err)
		return
	}

	// Two ways a run names this issue, and a real clone has rows from both:
	//
	//   * a.mission_id (#2256) — every assignment-creating path stamps it
	//     going forward, including a mention dispatch, which has no
	//     mission_tasks row at all.
	//   * mission_tasks.assignment_id — the pre-#2256 link, still the only
	//     one a row written before this column existed can be found by.
	//
	// DISTINCT because a mission-task run satisfies both after the backfill
	// / going-forward write, and the UNION-shaped OR would otherwise return
	// it twice.
	//
	// source is derived with two correlated EXISTS subqueries rather than a
	// LEFT JOIN: a JOIN against mission_tasks/mission_comment_mentions can
	// widen the DISTINCT row set if either table ever carries more than one
	// matching row for the same assignment_id (no UNIQUE constraint forbids
	// it), which would silently duplicate a run. EXISTS can't.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT DISTINCT a.id, a.status, a.started_at, a.finished_at, a.result_summary,
		       a.error_message, a.task, COALESCE(ag.name, ''), a.mission_id,
		       CASE
		         WHEN EXISTS (SELECT 1 FROM mission_tasks mt
		                      WHERE mt.assignment_id = a.id AND mt.mission_id = ?) THEN 'task'
		         WHEN EXISTS (SELECT 1 FROM mission_comment_mentions mcm
		                      WHERE mcm.assignment_id = a.id AND mcm.mission_id = ?) THEN 'mention'
		         ELSE 'delegation'
		       END AS source
		FROM assignments a
		LEFT JOIN agents ag ON ag.id = a.assigned_to_id
		WHERE a.workspace_id = ?
		  AND (
		        a.mission_id = ?
		        OR a.id IN (SELECT assignment_id FROM mission_tasks
		                     WHERE mission_id = ? AND assignment_id IS NOT NULL)
		      )
		ORDER BY COALESCE(a.started_at, a.created_at) DESC
		LIMIT 100`, missionID, missionID, wsID, missionID, missionID)
	if err != nil {
		internalError(w, r, h.logger, "issue runs: query", err)
		return
	}
	defer rows.Close()

	out := []issueRunDTO{}
	for rows.Next() {
		var (
			dto                                     issueRunDTO
			started, finished, result, errMsg, task sql.NullString
			missionIDCol                            sql.NullString
		)
		if err := rows.Scan(&dto.ID, &dto.Status, &started, &finished, &result,
			&errMsg, &task, &dto.AgentName, &missionIDCol, &dto.Source); err != nil {
			internalError(w, r, h.logger, "issue runs: scan", err)
			return
		}
		dto.StartedAt = started.String
		dto.EndedAt = finished.String
		dto.Task = task.String
		if missionIDCol.Valid && missionIDCol.String != "" {
			mid := missionIDCol.String
			dto.MissionID = &mid
		}
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

	writeJSON(w, http.StatusOK, out)
}
