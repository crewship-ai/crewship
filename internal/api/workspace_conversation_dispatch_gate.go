package api

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/groupchat"
)

// authorizeConversationAssignment is the final gate for queued channel work.
// Membership may have changed since a mention became an assignment. Other
// assignments retain their existing authorization paths.
func (h *AssignmentHandler) authorizeConversationAssignment(ctx context.Context, assignmentID string) (bool, error) {
	var jobID string
	err := h.db.QueryRowContext(ctx, `SELECT id FROM workspace_conversation_agent_jobs WHERE assignment_id=?`, assignmentID).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	release, err := groupchat.AcquireWriteAdmission(ctx, h.db)
	if err != nil {
		return false, err
	}
	defer release()
	c, err := h.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	defer c.ExecContext(context.Background(), "ROLLBACK")
	var status string
	err = c.QueryRowContext(ctx, `SELECT a.status FROM workspace_conversation_agent_jobs j
 JOIN assignments assignment ON assignment.id=j.assignment_id
 JOIN workspace_conversations conversation ON conversation.id=j.conversation_id
 JOIN workspaces w ON w.id=conversation.workspace_id
 JOIN workspace_members wm ON wm.workspace_id=w.id AND wm.user_id=j.requested_by_user_id
 JOIN workspace_conversation_agents ca ON ca.conversation_id=conversation.id AND ca.agent_id=j.agent_id
 JOIN agents a ON a.id=j.agent_id AND a.workspace_id=w.id
 JOIN crews crew ON crew.id=a.crew_id AND crew.workspace_id=w.id
 WHERE j.id=? AND assignment.id=? AND j.state='queued' AND conversation.kind='channel'
 AND conversation.deleted_at IS NULL AND w.deleted_at IS NULL AND a.deleted_at IS NULL AND crew.deleted_at IS NULL
 AND assignment.workspace_id=w.id AND assignment.assigned_to_id=a.id
 AND assignment.created_by_user_id=j.requested_by_user_id
 AND assignment.status IN ('PENDING','QUEUED','RUNNING') AND assignment.cancel_requested_at IS NULL`, jobID, assignmentID).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = c.ExecContext(ctx, `UPDATE assignments SET status='CANCELLED',cancel_requested_at=COALESCE(cancel_requested_at,?),finished_at=COALESCE(finished_at,?) WHERE id=? AND status IN ('PENDING','QUEUED','RUNNING')`, isoMillisNow(), isoMillisNow(), assignmentID); err != nil {
			return false, err
		}
		if _, err = c.ExecContext(ctx, `UPDATE workspace_conversation_agent_jobs SET state='failed',error='Conversation or agent access was revoked',updated_at=? WHERE id=? AND state IN ('pending','queued')`, isoMillisNow(), jobID); err != nil {
			return false, err
		}
		_, err = c.ExecContext(ctx, "COMMIT")
		return false, err
	}
	if status == chatbridge.AgentStatusPendingReview {
		if _, err = c.ExecContext(ctx, `UPDATE assignments SET status='QUEUED',queued_at=datetime('now','subsec'),queued_reason='agent_held' WHERE id=? AND status IN ('PENDING','RUNNING')`, assignmentID); err != nil {
			return false, err
		}
		_, err = c.ExecContext(ctx, "COMMIT")
		return false, err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return err == nil, err
}
