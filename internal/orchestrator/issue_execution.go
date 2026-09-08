package orchestrator

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/untrusted"
)

// checkIssueExecution drives durable work -> lead review -> correction rounds.
// The INSERT of a review task and the stage transition commit together; a
// process restart or two completion callbacks cannot schedule it twice.
func (e *MissionEngine) checkIssueExecution(ctx context.Context, ms *missionState) (bool, error) {
	var id string
	err := e.db.QueryRowContext(ctx, `SELECT id FROM issue_executions WHERE mission_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, ms.ID).Scan(&id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE issue_executions SET updated_at=updated_at WHERE id=?`, id); err != nil {
		return true, err
	}
	var stage, reviewer, status, mode, reviewTask, title, description, routineRun, created string
	var brief, work, currentBrief, currentWork, attempt int
	var clientReview bool
	err = tx.QueryRowContext(ctx, `SELECT x.stage,x.reviewer_agent_id,COALESCE(x.review_task_id,''),x.brief_revision,x.work_revision,x.attempt,
 m.status,m.title,COALESCE(m.description,''),w.mode,w.brief_revision,w.revision,w.client_review_required,COALESCE(x.routine_run_id,''),x.created_at
 FROM issue_executions x JOIN missions m ON m.id=x.mission_id JOIN issue_work w ON w.mission_id=m.id WHERE x.id=?`, id).
		Scan(&stage, &reviewer, &reviewTask, &brief, &work, &attempt, &status, &title, &description, &mode, &currentBrief, &currentWork, &clientReview, &routineRun, &created)
	if err != nil {
		return true, err
	}
	if status != "IN_PROGRESS" || mode == "human" {
		return true, nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if brief != currentBrief || work != currentWork || stage == "superseded" {
		return true, e.settleIssueExecution(ctx, tx, ms, id, "superseded", "TODO", "The assignment or handoff changed. Start a new execution to review the current revision.", now)
	}
	if stage != "working" && stage != "reviewing" {
		return true, nil
	}
	var routineEvidence string
	if routineRun != "" {
		var runStatus, runOutcome, output string
		err = tx.QueryRowContext(ctx, `SELECT status,COALESCE(outcome,''),COALESCE(output,'') FROM pipeline_runs WHERE id=? AND workspace_id=?`, routineRun, ms.WorkspaceID).Scan(&runStatus, &runOutcome, &output)
		if err == sql.ErrNoRows {
			started, _ := time.Parse(time.RFC3339Nano, created)
			if time.Since(started) > 2*time.Minute {
				return true, e.settleIssueExecution(ctx, tx, ms, id, "failed", "FAILED", "Routine dispatch was interrupted before a run was recorded. Start again to retry.", now)
			}
			return true, nil
		}
		if err != nil {
			return true, err
		}
		if runStatus == "queued" || runStatus == "running" || runStatus == "waiting" {
			return true, nil
		}
		if runStatus != "completed" || (runOutcome != OutcomeSucceeded && runOutcome != OutcomeNoChange && runOutcome != OutcomeWorkCreated) {
			return true, e.settleIssueExecution(ctx, tx, ms, id, "failed", "FAILED", "The bound routine did not complete successfully. Inspect its run before retrying.", now)
		}
		if len(output) > 16000 {
			output = output[:16000] + "\n(output preview truncated; inspect the run for full evidence)"
		}
		routineEvidence = "Routine run " + routineRun + " result:\n" + output + "\n"
	}

	if stage == "reviewing" {
		var result, outcome, runStatus string
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(a.result_summary,''),COALESCE(a.outcome,''),a.status FROM mission_tasks t JOIN assignments a ON a.id=t.assignment_id WHERE t.id=?`, reviewTask).Scan(&result, &outcome, &runStatus)
		if err == sql.ErrNoRows {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		if runStatus != "COMPLETED" && runStatus != "FAILED" && runStatus != "CANCELLED" && runStatus != "TIMEOUT" {
			return true, nil
		}
		verdict, target := issueReviewVerdict(result)
		handoff := ParseHandoff(result)
		if runStatus != "COMPLETED" || outcome != OutcomeSucceeded || !handoff.Parsed || verdict == "" {
			return true, e.settleIssueExecution(ctx, tx, ms, id, "failed", "FAILED", "Lead review did not produce a valid decision. Inspect the review run and retry or take over.", now)
		}
		if verdict == "approve" {
			var bad int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE issue_execution_id=? AND id<>(SELECT assignment_id FROM mission_tasks WHERE id=?) AND (status<>'COMPLETED' OR COALESCE(outcome,'') NOT IN ('SUCCEEDED','NO_CHANGE','WORK_CREATED'))`, id, reviewTask).Scan(&bad); err != nil {
				return true, err
			}
			if bad > 0 {
				return true, e.settleIssueExecution(ctx, tx, ms, id, "needs_human", "REVIEW", "Lead approved despite unsuccessful worker results. Human verification is required.\n"+handoff.Summary, now)
			}
			next := "DONE"
			if clientReview {
				next = "REVIEW"
			}
			return true, e.settleIssueExecution(ctx, tx, ms, id, "accepted", next, handoff.Summary, now)
		}
		if verdict == "needs_human" || attempt >= 2 {
			return true, e.settleIssueExecution(ctx, tx, ms, id, "needs_human", "REVIEW", "Human review required: "+handoff.Summary, now)
		}
		// The reviewer can return work only to a live worker from this execution.
		var worker string
		err = tx.QueryRowContext(ctx, `SELECT a.id FROM agents a WHERE a.slug=? AND a.workspace_id=? AND a.deleted_at IS NULL AND EXISTS(SELECT 1 FROM assignments s WHERE s.issue_execution_id=? AND s.assigned_to_id=a.id AND s.id<>(SELECT assignment_id FROM mission_tasks WHERE id=?))`, target, ms.WorkspaceID, id, reviewTask).Scan(&worker)
		if err == sql.ErrNoRows {
			return true, e.settleIssueExecution(ctx, tx, ms, id, "needs_human", "REVIEW", "Lead requested changes without a valid worker from this execution.\n"+handoff.Summary, now)
		}
		if err != nil {
			return true, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE issue_executions SET stage='changes_requested',verdict='request_changes',review_note=?,updated_at=? WHERE id=?`, handoff.Summary, now, id); err != nil {
			return true, err
		}
		nextID := generateID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO issue_executions(id,mission_id,work_revision,brief_revision,attempt,stage,reviewer_agent_id,created_at,updated_at) VALUES(?,?,?,?,?,'working',?,?,?)`, nextID, ms.ID, work, brief, attempt+1, reviewer, now, now); err != nil {
			return true, err
		}
		note := description + "\n\nLead requested these corrections to the previous result:\n" + handoff.Summary
		if err = insertIssueExecutionTask(ctx, tx, ms.ID, nextID, worker, title, note, now); err != nil {
			return true, err
		}
		if err = e.issueExecutionComment(ctx, tx, ms.ID, reviewer, "Lead requested changes: "+handoff.Summary, now); err != nil {
			return true, err
		}
		if err = tx.Commit(); err != nil {
			return true, err
		}
		e.broadcastMissionStatus(ms, "IN_PROGRESS")
		return true, nil
	}
	var pending, total int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT') THEN 1 ELSE 0 END),0) FROM assignments WHERE issue_execution_id=?`, id).Scan(&total, &pending)
	if err != nil {
		return true, err
	}
	if (total == 0 && routineRun == "") || pending > 0 {
		return true, nil
	}
	var pendingTasks int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_tasks WHERE issue_execution_id=? AND status NOT IN ('COMPLETED','FAILED','SKIPPED','CANCELLED')`, id).Scan(&pendingTasks); err != nil {
		return true, err
	}
	if pendingTasks > 0 {
		return true, nil
	}
	// All workers are terminal. Include evidence even on failure so the Lead can
	// request an explicit corrective step, never silently accept a crashed run.
	rows, err := tx.QueryContext(ctx, `SELECT a.slug,s.status,COALESCE(s.outcome,''),substr(COALESCE(s.result_summary,''),1,10000) FROM assignments s JOIN agents a ON a.id=s.assigned_to_id WHERE s.issue_execution_id=? ORDER BY s.created_at,s.id LIMIT 30`, id)
	if err != nil {
		return true, err
	}
	var evidence strings.Builder
	evidence.WriteString(routineEvidence)
	for rows.Next() {
		var slug, st, oc, result string
		if err = rows.Scan(&slug, &st, &oc, &result); err != nil {
			rows.Close()
			return true, err
		}
		fmt.Fprintf(&evidence, "Worker %s, process %s, outcome %s:\n%s\n", slug, st, oc, result)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return true, err
	}
	taskID := generateID()
	prompt := "Review this issue against its acceptance criteria. Verify the actual deliverables; a process exit or a worker's confidence is not evidence. Do not implement fixes or delegate in this review turn. Return request_changes naming the worker who must correct the result, or needs_human for a human decision. Approve only verified complete work.\n" +
		untrusted.Wrap("issue_brief", description) + "\n" + untrusted.Wrap("worker_results", evidence.String()) + `
End with the standard HANDOFF block, outcome SUCCEEDED when the review itself is complete, and include these two fields INSIDE that block:
review: <approve|request_changes|needs_human>
review_target: <worker slug when requesting changes; otherwise none>
The summary must state what you checked and specific corrections or remaining blockers.
`
	if _, err = tx.ExecContext(ctx, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,description,status,task_order,depends_on,issue_execution_id,created_at,updated_at) SELECT ?,?,?,?,?,'PENDING',COALESCE(MAX(task_order),0)+1,'[]',?,?,? FROM mission_tasks WHERE mission_id=?`, taskID, ms.ID, reviewer, "Lead review: "+title, prompt, id, now, now, ms.ID); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE issue_executions SET stage='reviewing',review_task_id=?,updated_at=? WHERE id=?`, taskID, now, id); err != nil {
		return true, err
	}
	if err = e.issueExecutionComment(ctx, tx, ms.ID, reviewer, "Worker results received. Lead review is queued.", now); err != nil {
		return true, err
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	e.broadcastMissionStatus(ms, "IN_PROGRESS")
	return true, nil
}

func insertIssueExecutionTask(ctx context.Context, tx *sql.Tx, missionID, executionID, worker, title, note, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,description,status,task_order,depends_on,issue_execution_id,created_at,updated_at) SELECT ?,?,?,?,?,'PENDING',COALESCE(MAX(task_order),0)+1,'[]',?,?,? FROM mission_tasks WHERE mission_id=?`, generateID(), missionID, worker, title, note, executionID, now, now, missionID)
	return err
}
func issueReviewVerdict(result string) (string, string) {
	start := strings.LastIndex(result, "---HANDOFF---")
	if start < 0 {
		return "", ""
	}
	block := result[start:]
	end := strings.Index(block, "---END HANDOFF---")
	if end < 0 {
		return "", ""
	}
	block = block[:end]
	var verdict, target string
	var seenVerdict, seenTarget bool
	for _, line := range strings.Split(block, "\n") {
		if v, ok := cutPrefixFold(strings.TrimSpace(line), "review:"); ok {
			if seenVerdict {
				return "", ""
			}
			seenVerdict = true
			verdict = strings.TrimSpace(v)
		}
		if v, ok := cutPrefixFold(strings.TrimSpace(line), "review_target:"); ok {
			if seenTarget {
				return "", ""
			}
			seenTarget = true
			target = strings.TrimSpace(v)
		}
	}
	if verdict != "approve" && verdict != "request_changes" && verdict != "needs_human" {
		verdict = ""
	}
	return verdict, target
}
func (e *MissionEngine) issueExecutionComment(ctx context.Context, tx *sql.Tx, missionID, agentID, body, now string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at) VALUES(?,?,'agent',?,?,?,?)`, generateID(), missionID, agentID, body, now, now)
	return err
}
func (e *MissionEngine) settleIssueExecution(ctx context.Context, tx *sql.Tx, ms *missionState, id, stage, status, note, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE issue_executions SET stage=?,verdict=?,review_note=?,updated_at=? WHERE id=?`, stage, stage, note, now, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE missions SET status=?,updated_at=?,completed_at=CASE WHEN ?='DONE' THEN ? ELSE NULL END WHERE id=? AND status='IN_PROGRESS'`, status, now, status, now, ms.ID); err != nil {
		return err
	}
	if err := e.issueExecutionComment(ctx, tx, ms.ID, ms.LeadAgentID, note, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.broadcastMissionStatus(ms, status)
	if status == "REVIEW" {
		e.fireIssueReviewInboxNotification(ctx, ms)
	}
	return nil
}
