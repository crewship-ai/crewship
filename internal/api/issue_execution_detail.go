package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

type issueExecutionResponse struct {
	ID           string                 `json:"id"`
	Stage        string                 `json:"stage"`
	Attempt      int                    `json:"attempt"`
	Reviewer     string                 `json:"reviewer"`
	Note         string                 `json:"note"`
	RoutineRunID string                 `json:"routine_run_id,omitempty"`
	Workers      []issueExecutionWorker `json:"workers"`
}
type issueExecutionWorker struct {
	AssignmentID string `json:"assignment_id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Outcome      string `json:"outcome"`
	RunID        string `json:"run_id,omitempty"`
	Review       bool   `json:"review"`
}

func (h *IssueHandler) loadIssueExecution(ctx context.Context, issue *issueResponse) error {
	// A mission with no issue_work row is not an error. The row arrives from
	// the migration's backfill and the AFTER INSERT trigger, so anything that
	// writes a mission around those — a restore, a mission_type flipped by an
	// UPDATE — leaves a row this read must survive. Degrading to the zero
	// values keeps the whole issue readable; returning ErrNoRows would turn
	// every such detail request into a 500.
	if err := h.db.QueryRowContext(ctx, `SELECT brief_revision,client_review_required FROM issue_work WHERE mission_id=?`, issue.ID).Scan(&issue.BriefRevision, &issue.ClientReviewRequired); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	execution := &issueExecutionResponse{Workers: []issueExecutionWorker{}}
	var reviewTask sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT x.id,x.stage,x.attempt,a.name,x.review_note,COALESCE(x.routine_run_id,''),x.review_task_id FROM issue_executions x JOIN agents a ON a.id=x.reviewer_agent_id WHERE x.mission_id=? ORDER BY x.created_at DESC,x.id DESC LIMIT 1`, issue.ID).Scan(&execution.ID, &execution.Stage, &execution.Attempt, &execution.Reviewer, &execution.Note, &execution.RoutineRunID, &reviewTask)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT a.id,agent.name,CASE WHEN a.status='RUNNING' AND COALESCE(a.exec_id,'')='' THEN 'PREPARING' ELSE a.status END,COALESCE(a.outcome,''),COALESCE((SELECT je.trace_id FROM journal_entries je WHERE je.mission_id=a.mission_id AND je.entry_type='run.started' AND json_extract(je.payload,'$.assignment_id')=a.id ORDER BY je.ts DESC LIMIT 1),''),EXISTS(SELECT 1 FROM mission_tasks t WHERE t.id=? AND t.assignment_id=a.id) FROM assignments a JOIN agents agent ON agent.id=a.assigned_to_id WHERE a.issue_execution_id=? ORDER BY a.created_at DESC,a.id DESC LIMIT 50`, reviewTask, execution.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var worker issueExecutionWorker
		if err = rows.Scan(&worker.AssignmentID, &worker.Name, &worker.Status, &worker.Outcome, &worker.RunID, &worker.Review); err != nil {
			return err
		}
		execution.Workers = append(execution.Workers, worker)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	issue.Execution = execution
	return nil
}

// ReviewPolicy can change only between executions. The version CAS prevents a
// stale browser from silently removing client acceptance on newer work.
func (h *IssueHandler) ReviewPolicy(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	var req struct {
		Revision *int  `json:"revision"`
		Required *bool `json:"client_review_required"`
	}
	if readJSON(r, &req) != nil || req.Revision == nil || req.Required == nil {
		writeProblem(w, r, 400, "revision and client_review_required are required")
		return
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE issue_work SET client_review_required=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE revision=? AND mission_id IN (SELECT id FROM missions WHERE identifier=? AND crew_id=? AND workspace_id=? AND status IN ('TODO','BACKLOG')) AND NOT EXISTS(SELECT 1 FROM assignments a WHERE a.mission_id=issue_work.mission_id AND a.status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT'))`, *req.Required, *req.Revision, r.PathValue("identifier"), r.PathValue("crewId"), WorkspaceIDFromContext(r.Context()))
	if err != nil {
		internalError(w, r, h.logger, "review policy", err)
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		writeProblem(w, r, 409, "Refresh the issue. Acceptance can change only before work starts.")
		return
	}
	h.broadcastIssueEvent(WorkspaceIDFromContext(r.Context()), "issue.updated", map[string]string{"identifier": r.PathValue("identifier")})
	writeJSON(w, 200, map[string]any{"revision": *req.Revision + 1, "client_review_required": *req.Required})
}
