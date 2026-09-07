package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/missionactivity"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Work changes the current worker without changing the accountable owner.
// Revision checks, the handoff note, cancellation and the audit receipt commit
// together. Retrying an operation never cancels or transfers work twice.
func (h *IssueHandler) Work(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		writeProblem(w, r, 401, "Sign in to transfer work")
		return
	}
	h.workAs(w, r, "user", user.ID, WorkspaceIDFromContext(r.Context()))
}

func (h *IssueHandler) workAs(w http.ResponseWriter, r *http.Request, actorType, actorID, wsID string) {
	var req struct {
		OperationID string `json:"operation_id"`
		Revision    *int   `json:"revision"`
		Action      string `json:"action"`
		TargetID    string `json:"target_id"`
		Note        string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, 400, "Invalid JSON body")
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Revision == nil || req.OperationID == "" || len(req.OperationID) > 100 || len(req.Note) > 20000 {
		writeProblem(w, r, 400, "operation_id, revision and a note of at most 20000 characters are required")
		return
	}
	if req.Action != "take_over" && req.Action != "handoff_agent" && req.Action != "handoff_human" && req.Action != "submit" {
		writeProblem(w, r, 400, "Unknown work action")
		return
	}
	if req.Action != "take_over" && req.Note == "" {
		writeProblem(w, r, 400, "Describe the result or what the next worker should do")
		return
	}
	if actorType == "agent" && req.Action != "handoff_agent" && req.Action != "handoff_human" {
		writeProblem(w, r, 403, "Agents may hand off work; human takeover and submission require a person")
		return
	}
	if req.Action == "take_over" {
		req.TargetID = actorID
	}
	raw, _ := json.Marshal(req)
	hash := fmt.Sprintf("%x", sha256.Sum256(append(raw, []byte(actorType+":"+actorID)...)))
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		internalError(w, r, h.logger, "work: begin", err)
		return
	}
	defer tx.Rollback()
	var id, mode, status string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT m.id,w.mode,w.revision,m.status FROM missions m JOIN issue_work w ON w.mission_id=m.id WHERE m.identifier=? AND m.crew_id=? AND m.workspace_id=?`, r.PathValue("identifier"), r.PathValue("crewId"), wsID).Scan(&id, &mode, &revision, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, 404, "Issue not found")
		return
	}
	if err != nil {
		internalError(w, r, h.logger, "work: load", err)
		return
	}
	var receipt string
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM mission_activity WHERE mission_id=? AND source_kind='work_operation' AND source_id=?`, id, req.OperationID).Scan(&receipt)
	if err == nil {
		var saved struct {
			Hash     string `json:"hash"`
			Revision int    `json:"revision"`
		}
		if json.Unmarshal([]byte(receipt), &saved) != nil || saved.Hash != hash {
			writeProblem(w, r, 409, "Operation ID already used for a different request")
			return
		}
		writeJSON(w, 200, map[string]any{"revision": saved.Revision, "replayed": true})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		internalError(w, r, h.logger, "work: receipt", err)
		return
	}
	if revision != *req.Revision {
		writeProblem(w, r, 409, "Work changed. Refresh the issue before transferring it.")
		return
	}

	if actorType == "agent" {
		var delegate sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT delegate_agent_id FROM missions WHERE id=?`, id).Scan(&delegate); err != nil {
			internalError(w, r, h.logger, "work: delegate", err)
			return
		}
		if mode == "human" || delegate.String != actorID {
			writeProblem(w, r, 403, "Only the current agent delegate may hand off this work")
			return
		}
	}
	if status == "DONE" || status == "COMPLETED" || status == "CANCELLED" || status == "DUPLICATE" {
		writeProblem(w, r, 409, "Reopen this issue before transferring work")
		return
	}
	var active int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignments WHERE `+assignmentMatch, id, id, id).Scan(&active)
	if err != nil {
		internalError(w, r, h.logger, "work: active runs", err)
		return
	}
	if (req.Action == "handoff_agent" || req.Action == "submit") && active > 0 {
		writeProblem(w, r, 409, "Wait for active runs to stop before handing back or submitting work")
		return
	}
	nextMode := "human"
	var worker any = req.TargetID
	if req.Action == "handoff_agent" {
		nextMode = "agent"
		worker = nil
	}
	if req.Action == "submit" {
		if mode != "human" {
			writeProblem(w, r, 409, "Only human work can be submitted here")
			return
		}
		nextMode = mode
	} else {
		targetType := "user"
		if nextMode == "agent" {
			targetType = "agent"
		}
		if req.TargetID == "" {
			writeProblem(w, r, 400, "Choose a worker")
			return
		}
		if ok, err := validateAssigneeWorkspace(ctx, tx, targetType, req.TargetID, wsID); err != nil || !ok {
			writeProblem(w, r, 400, "Worker is not available in this workspace")
			return
		}
	}

	targetName := ""
	if req.Action != "submit" {
		var name sql.NullString
		if nextMode == "human" {
			err = tx.QueryRowContext(ctx, `SELECT full_name FROM users WHERE id=?`, req.TargetID).Scan(&name)
		} else {
			var agentStatus string
			err = tx.QueryRowContext(ctx, `SELECT name,COALESCE(status,'') FROM agents WHERE id=? AND workspace_id=?`, req.TargetID, wsID).Scan(&name, &agentStatus)
			if err == nil && agentStatus == "PENDING_REVIEW" {
				writeProblem(w, r, 409, "This agent is awaiting approval and cannot take work yet")
				return
			}
		}
		if err != nil {
			internalError(w, r, h.logger, "work: resolve target", err)
			return
		}
		targetName = name.String
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var targets []cancelTarget
	if nextMode == "human" && req.Action != "submit" {
		targets, err = holdIssueWorkTx(ctx, tx, id, req.TargetID, req.Note, now)
	}
	if err != nil {
		internalError(w, r, h.logger, "work: stop previous work", err)
		return
	}
	if req.Action == "submit" {
		_, err = tx.ExecContext(ctx, `UPDATE issue_work SET revision=revision+1,note=?,updated_at=? WHERE mission_id=?`, req.Note, now, id)
	} else if nextMode != "human" {
		_, err = tx.ExecContext(ctx, `UPDATE issue_work SET mode=?,worker_user_id=?,revision=revision+1,note=?,updated_at=? WHERE mission_id=?`, nextMode, worker, req.Note, now, id)
	}
	if err == nil && req.Action == "handoff_agent" {
		_, err = tx.ExecContext(ctx, `UPDATE missions SET delegate_agent_id=?,assignee_type='agent',assignee_id=? WHERE id=?`, req.TargetID, req.TargetID, id)
	}

	if err == nil && req.Action == "handoff_agent" {
		// Pending messages to the previous worker must not revive after handoff.
		_, err = tx.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='superseded',dispatch_detail='Work handed to another worker' WHERE mission_id=? AND state='pending'`, id)
		if err != nil {
			internalError(w, r, h.logger, "work: supersede messages", err)
			return
		}
		// Preserve completed work and start a new step with the explicit handoff.
		_, err = tx.ExecContext(ctx, `UPDATE mission_tasks SET status='SKIPPED',updated_at=? WHERE mission_id=? AND status NOT IN ('COMPLETED','SKIPPED')`, now, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO mission_tasks(id,mission_id,assigned_agent_id,title,description,status,task_order,depends_on,created_at,updated_at)
 SELECT ?,m.id,?,m.title,COALESCE(m.description,'') || char(10) || char(10) || 'Handoff:' || char(10) || ?,'PENDING',COALESCE((SELECT MAX(task_order)+1 FROM mission_tasks WHERE mission_id=m.id),1),'[]',?,? FROM missions m WHERE m.id=?`, generateCUID(), req.TargetID, req.Note, now, now, id)
		}
	}
	nextStatus := "TODO"
	if req.Action == "submit" {
		nextStatus = "REVIEW"
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE missions SET status=?,completed_at=NULL,updated_at=? WHERE id=?`, nextStatus, now, id)
	}
	if err == nil {
		body := "Handed work to " + targetName + ".\n\n" + req.Note
		if req.Action == "submit" {
			body = "Result submitted for review.\n\n" + req.Note
		}
		if req.Action == "take_over" {
			body = targetName + " took over this issue. Automatic agent work is paused.\n\n" + req.Note
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, generateCUID(), id, actorType, actorID, body, now, now)
	}
	// A handoff to a person must reach that person's in-product Inbox.
	// Keep it in the same transaction as the work receipt, so retries cannot
	// lose or duplicate the request. This does not send external messages.
	notificationID := ""
	if err == nil && req.Action == "handoff_human" {
		notificationID = generateCUID()
		itemPayload, _ := json.Marshal(map[string]string{"issue_identifier": r.PathValue("identifier"), "mission_id": id})
		stamp := tsformat.Format(time.Now())
		_, err = tx.ExecContext(ctx, `INSERT INTO inbox_items(id,workspace_id,kind,source_id,target_user_id,title,body_md,sender_type,sender_id,blocking,payload_json,created_at,updated_at) VALUES(?,?,'message',?,?,?,?,?,?,0,?,?,?)`, notificationID, wsID, "issue-work:"+id+":"+req.OperationID, req.TargetID, "Work handed to you: "+r.PathValue("identifier"), req.Note, actorType, actorID, string(itemPayload), stamp, stamp)
	}
	payload, _ := json.Marshal(map[string]any{"hash": hash, "revision": revision + 1, "action": req.Action, "target_id": req.TargetID, "note": req.Note})
	if err == nil {
		_, err = missionactivity.EmitTx(ctx, tx, missionactivity.Entry{ID: generateCUID(), MissionID: id, ActorType: actorType, ActorID: actorID, Action: "assignee_changed", Details: req.Note, PayloadJSON: string(payload), SourceKind: "work_operation", SourceID: req.OperationID})
	}
	if err != nil {
		internalError(w, r, h.logger, "work: persist", err)
		return
	}
	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "work: commit", err)
		return
	}
	if nextMode == "human" {
		if engine, ok := h.missionEngine.(interface{ StopMission(string) }); ok {
			engine.StopMission(id)
		}
	}
	if len(targets) > 0 {
		h.hardStopTargets(ctx, wsID, r.PathValue("identifier"), targets)
	}
	if notificationID != "" {
		broadcastWorkspaceEvent(h.hub, wsID, "inbox.updated", map[string]string{"id": notificationID})
	}
	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": id, "identifier": r.PathValue("identifier")})
	writeJSON(w, 200, map[string]any{"revision": revision + 1, "status": nextStatus})
}

// holdIssueWorkTx is shared by issue handoffs and Inbox Take over.
// Keep the worker fence, cancellation and revision in the caller's transaction.
func holdIssueWorkTx(ctx context.Context, tx *sql.Tx, id, userID, note, now string) ([]cancelTarget, error) {
	targets, err := stampCancelRequested(ctx, tx, now, id)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE assignments SET status='CANCELLED',finished_at=?,outcome='CANCELLED' WHERE `+assignmentMatch+` AND status!='RUNNING'`, now, id, id, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE mission_tasks SET status='CANCELLED',updated_at=? WHERE mission_id=? AND status NOT IN ('COMPLETED','FAILED','CANCELLED','SKIPPED')`, now, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='superseded',dispatch_detail='Work transferred to a human' WHERE mission_id=? AND state='pending'`, id)
	}
	if err == nil {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE issue_work SET mode='human',worker_user_id=?,revision=revision+1,note=?,updated_at=? WHERE mission_id=?`, userID, note, now, id)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				err = fmt.Errorf("issue has no work state")
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE missions SET status='TODO',completed_at=NULL,updated_at=? WHERE id=?`, now, id)
	}
	return targets, err
}
