package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

// Assignment projection is a separate durable outbox from the run journal:
// acknowledging a run receipt must never discard an unprojected domain result.

func (rt *agentWorkRuntime) FlushRunOutcomes(ctx context.Context) error {
	return errors.Join(rt.WebhookRuntime.FlushRunOutcomes(ctx), rt.assignment.Flush(ctx))
}

func (rt *AssignmentRuntime) Flush(ctx context.Context) error {
	if rt == nil || rt.h == nil {
		return nil
	}
	if !rt.projectionMu.TryLock() {
		return nil
	}
	defer rt.projectionMu.Unlock()
	// Cancellation stamps are intent, not permission for a terminal domain write.
	rows, err := rt.h.db.QueryContext(ctx, `SELECT w.id FROM work_items w JOIN assignments a ON a.id=w.domain_id AND a.workspace_id=w.workspace_id WHERE w.source='assignment' AND w.domain_kind='assignment' AND a.cancel_requested_at IS NOT NULL AND w.state NOT IN ('succeeded','failed','cancelled','expired') AND NOT EXISTS(SELECT 1 FROM work_attempts t WHERE t.work_id=w.id AND t.generation=w.generation AND t.cancel_requested_at IS NOT NULL) LIMIT 100`)
	if err != nil {
		return err
	}
	var cancelIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		cancelIDs = append(cancelIDs, id)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return err
	}
	store := work.NewStore(rt.h.db)
	for _, id := range cancelIDs {
		if _, err = store.RequestCancel(ctx, id, "assignment", "assignment cancellation requested"); err != nil {
			return err
		}
	}
	rows, err = rt.h.db.QueryContext(ctx, `SELECT a.id FROM assignments a JOIN work_items w ON w.domain_id=a.id AND w.workspace_id=a.workspace_id WHERE w.source='assignment' AND w.domain_kind='assignment' AND w.state IN ('succeeded','failed','cancelled','expired') AND a.work_projected_at IS NULL ORDER BY COALESCE(a.work_projection_attempted_at,a.created_at),a.id LIMIT 100`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err())
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err = rt.project(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("project assignment %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

func (rt *AssignmentRuntime) project(ctx context.Context, id string) error {
	h := rt.h
	if _, err := h.db.ExecContext(ctx, `UPDATE assignments SET work_projection_attempted_at=? WHERE id=? AND work_projected_at IS NULL`, isoMillisNow(), id); err != nil {
		return err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ws, agent, crew, slug, chat, author, state, reason, runID, stamp string
	var resultJSON sql.NullString
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT w.workspace_id,w.agent_id,COALESCE(w.crew_id,''),COALESCE(ag.slug,''),COALESCE(a.chat_id,''),COALESCE(json_extract(w.input_json,'$.author_agent_id'),''),w.state,w.state_reason,COALESCE(t.run_id,''),COALESCE(t.ended_at,w.updated_at),t.run_result_json,w.generation
 FROM assignments a JOIN work_items w ON w.domain_id=a.id AND w.workspace_id=a.workspace_id
 LEFT JOIN agents ag ON ag.id=w.agent_id AND ag.workspace_id=w.workspace_id
 LEFT JOIN work_attempts t ON t.work_id=w.id AND t.generation=w.generation
 WHERE a.id=? AND a.work_projected_at IS NULL AND w.source='assignment' AND w.domain_kind='assignment' AND w.state IN ('succeeded','failed','cancelled','expired') AND (t.run_id IS NULL OR t.ended_at IS NOT NULL)`, id).Scan(&ws, &agent, &crew, &slug, &chat, &author, &state, &reason, &runID, &stamp, &resultJSON, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	status := "FAILED"
	if state == "succeeded" {
		status = "COMPLETED"
	}
	if state == "cancelled" {
		status = "CANCELLED"
	}
	var observed work.RunResult
	if resultJSON.Valid {
		if err = json.Unmarshal([]byte(resultJSON.String), &observed); err != nil {
			return err
		}
	}
	result, _ := observed.Metadata["assignment_result"].(string)
	if status == "COMPLETED" && (!resultJSON.Valid || observed.ExitCode == nil || *observed.ExitCode != 0 || (observed.ErrorMessage != nil && *observed.ErrorMessage != "")) {
		return errors.New("successful assignment lacks observed terminal result")
	}
	message := reason
	if observed.ErrorMessage != nil {
		message = *observed.ErrorMessage
	}
	if status == "COMPLETED" {
		message = ""
	}
	outcome, defaultReason := orchestrator.DeriveOutcome(status, orchestrator.ReportedOutcome(result), true)
	if message == "" {
		message = defaultReason
	}
	var oldStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, id).Scan(&oldStatus); err != nil {
		return err
	}
	if (oldStatus == "COMPLETED" || oldStatus == "FAILED" || oldStatus == "CANCELLED") && oldStatus != status {
		return errors.New("assignment terminal conflicts with work authority")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE assignments SET status=?,result_summary=?,error_message=NULLIF(?,''),finished_at=?,outcome=? WHERE id=?`, status, result, message, stamp, outcome, id); err != nil {
		return err
	}
	var session string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(session_id,'') FROM assignments WHERE id=?`, id).Scan(&session); err != nil {
		return err
	}
	if err = writeSessionCheckpoint(ctx, tx, ws, session, id, orchestrator.ParseCheckpoint(result)); err != nil {
		return err
	}
	if _, err = consumeDeliveriesForRun(ctx, tx, id); err != nil {
		return err
	}
	if session != "" {
		if _, err = tx.ExecContext(ctx, `UPDATE issue_agent_sessions SET state=?,active_run_id=NULL,last_activity_at=?,updated_at=? WHERE id=? AND workspace_id=? AND active_run_id=?`, orchestrator.RouteForOutcome(outcome).SessionState, stamp, stamp, session, ws, id); err != nil {
			return err
		}
	}
	if err = assignmentMissionCommentTx(ctx, tx, id, agent, status, outcome, result, message, stamp); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Stable receipt binds terminal lifecycle to the same persisted result. A
	// crash before the final marker retries this receipt and the callback.
	replay, ok := h.journal.(journal.ReplaySyncEmitter)
	if !ok {
		return errors.New("assignment projection requires replay-safe journal")
	}
	ts, err := parseDBTime(stamp)
	if err != nil {
		return err
	}
	kind := journal.EntryAssignmentDone
	severity := journal.SeverityInfo
	if status == "FAILED" {
		kind = journal.EntryAssignmentFail
		severity = journal.SeverityWarn
	}
	if status == "CANCELLED" {
		kind = journal.EntryAssignmentCancel
	}
	traceID := runID
	if traceID == "" {
		traceID = "assignment:" + id
	}
	entry := journal.Entry{ID: "assignment-terminal:" + id, SpanID: "assignment-terminal:" + id, TS: ts, WorkspaceID: ws, CrewID: crew, AgentID: agent, ActorType: journal.ActorOrchestrator, ActorID: author, Type: kind, Severity: severity, TraceID: traceID, Summary: fmt.Sprintf("assignment %s %s", shortRunID(id), strings.ToLower(status)), Refs: map[string]any{"assignment_id": id}, Payload: map[string]any{"assignment_id": id, "outcome": outcome, "error_message": message}}
	if _, _, err = replay.EmitReplaySync(ctx, entry); err != nil {
		return err
	}
	if h.missionCallback != nil {
		if err = h.missionCallback.OnAssignmentCompleted(ctx, id, status, result, message); err != nil {
			return err
		}
	}
	if orchestrator.RouteForOutcome(outcome).CreatesInboxItem {
		if err = h.createOutcomeInboxItem(ctx, id, ws, slug, result); err != nil {
			return err
		}
	}
	if status == "COMPLETED" {
		if err = h.publishIssueDeliverables(ctx, id, result); err != nil {
			return err
		}
	}
	if err = h.dispatchQueuedFollowUpsForSession(ctx, id, ws); err != nil {
		return err
	}
	if _, err = h.db.ExecContext(ctx, `UPDATE assignments SET work_projected_at=? WHERE id=? AND status=? AND work_projected_at IS NULL`, isoMillisNow(), id, status); err != nil {
		return err
	}
	if release, ok := rt.held.LoadAndDelete(runID); ok {
		release.(func())()
		rt.base.Forget("agent-run:" + runID)
	}
	if session != "" {
		broadcastIssueCheckpointWritten(ctx, h.db, h.hub, ws, session)
		broadcastIssueSessionState(ctx, h.db, h.hub, ws, session, orchestrator.RouteForOutcome(outcome).SessionState)
	}
	event := "assignment_completed"
	if status == "FAILED" {
		event = "assignment_failed"
	}
	if status == "CANCELLED" {
		event = "assignment_cancelled"
	}
	broadcastChannelEvent(h.hub, "session", chat, event, map[string]string{"id": id, "target": slug, "outcome": outcome})
	broadcastRunOutcome(ctx, h.db, h.hub, ws, id, status, outcome)
	return nil
}
