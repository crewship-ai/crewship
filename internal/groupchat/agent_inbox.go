package groupchat

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// AgentInboxMessage references canonical channel work. Reading this projection
// never acknowledges a message on behalf of the agent.
func InboxSourceDigest(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

type AgentInboxMessage struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	ConversationID string  `json:"conversation_id"`
	MessageID      string  `json:"message_id"`
	AssignmentID   string  `json:"assignment_id"`
	SenderID       string  `json:"sender_id"`
	Content        string  `json:"content"`
	State          string  `json:"state"`
	ReplyMessageID string  `json:"reply_message_id"`
	CreatedAt      string  `json:"created_at"`
	ReadAt         *string `json:"read_at"`
}

// Sender provenance is captured by Send in the accepting transaction. A new
// membership or account epoch cannot revive old requests after revocation.
const liveInboxJob = `c.kind='channel' AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM crews cr WHERE cr.id=a.crew_id AND cr.workspace_id=c.workspace_id AND cr.deleted_at IS NULL)
 AND EXISTS(SELECT 1 FROM workspace_conversation_agents ca WHERE ca.conversation_id=c.id AND ca.agent_id=a.id)
 AND EXISTS(SELECT 1 FROM workspace_members original JOIN users usr ON usr.id=original.user_id
 WHERE original.workspace_id=c.workspace_id AND original.user_id=j.requested_by_user_id AND usr.suspended_at IS NULL
 AND ((j.read_authority_json IS NULL AND j.read_source_sha256 IS NULL)
 OR (json_valid(j.read_authority_json) AND j.read_source_sha256 IS NOT NULL AND original.id=json_extract(j.read_authority_json,'$.member_id')
 AND original.access_revision=json_extract(j.read_authority_json,'$.member_revision')
 AND usr.authority_revision=json_extract(j.read_authority_json,'$.user_revision'))))`

func (s *Store) AgentInbox(ctx context.Context, w, u, agent string) ([]AgentInboxMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,c.id,m.id,COALESCE(j.assignment_id,''),COALESCE(m.author_user_id,''),m.content,COALESCE(execution.status,j.state),COALESCE(j.reply_message_id,''),j.created_at,receipt.read_at,receipt.source_sha256
 FROM workspace_conversation_agent_jobs j JOIN workspace_conversations c ON c.id=j.conversation_id
 JOIN workspace_conversation_messages m ON m.id=j.message_id AND m.conversation_id=c.id
 JOIN agents a ON a.id=j.agent_id LEFT JOIN assignments execution ON execution.id=j.assignment_id AND execution.workspace_id=c.workspace_id AND execution.assigned_to_id=a.id LEFT JOIN agent_inbox_receipts receipt ON receipt.workspace_id=c.workspace_id AND receipt.agent_id=a.id AND receipt.source_kind='channel_message' AND receipt.source_id=j.id
 WHERE a.id=? AND `+accessible+` AND `+liveInboxJob+` AND EXISTS(SELECT 1 FROM users viewer WHERE viewer.id=? AND viewer.suspended_at IS NULL) ORDER BY j.created_at DESC,j.id DESC LIMIT 50`, agent, w, u, u, u)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentInboxMessage{}
	for rows.Next() {
		var m AgentInboxMessage
		var read, digest sql.NullString
		m.Kind = "channel_message"
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.MessageID, &m.AssignmentID, &m.SenderID, &m.Content, &m.State, &m.ReplyMessageID, &m.CreatedAt, &read, &digest); err != nil {
			return nil, err
		}
		if read.Valid && digest.String == InboxSourceDigest(m.Content) {
			m.ReadAt = &read.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReadInboxAssignment runs inside the caller's write transaction. It returns
// only the canonical message of this exact assignment; it never scans another
// private session served by the same agent. Authority is checked in the SELECT.
func ReadInboxAssignment(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, w, crew, agent, assignment, run string) (job, content string, err error) {
	var expected sql.NullString
	err = q.QueryRowContext(ctx, `SELECT j.id,m.content,j.read_source_sha256 FROM workspace_conversation_agent_jobs j
 JOIN workspace_conversations c ON c.id=j.conversation_id
 JOIN workspace_conversation_messages m ON m.id=j.message_id AND m.conversation_id=c.id
 JOIN agents a ON a.id=j.agent_id
 JOIN assignments execution ON execution.id=j.assignment_id AND execution.workspace_id=c.workspace_id AND execution.assigned_to_id=a.id
 JOIN work_items source_work ON source_work.source='assignment' AND source_work.domain_id=execution.id AND source_work.workspace_id=c.workspace_id AND source_work.agent_id=a.id AND source_work.state IN ('starting','running')
 JOIN work_attempts source_attempt ON source_attempt.work_id=source_work.id AND source_attempt.generation=source_work.generation AND source_attempt.run_id=? AND source_attempt.ended_at IS NULL
 WHERE j.assignment_id=? AND a.id=? AND a.crew_id=? AND c.workspace_id=? AND c.deleted_at IS NULL AND execution.status='RUNNING' AND execution.cancel_requested_at IS NULL AND j.state='queued'
 AND EXISTS(SELECT 1 FROM workspaces w WHERE w.id=c.workspace_id AND w.deleted_at IS NULL) AND `+liveInboxJob, run, assignment, agent, crew, w).Scan(&job, &content, &expected)
	if err == nil && expected.Valid && expected.String != InboxSourceDigest(content) {
		return "", "", sql.ErrNoRows
	}
	return
}

// ValidateAgentJob is the shared live authority fence for preparation, final
// dispatch, read receipts and reply/retry publication. It never grants issue
// mutation authority: it only preserves the original channel request's epochs
// and immutable canonical bytes. A legacy job with two NULL provenance fields uses current authority only.
func ValidateAgentJob(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, job string) error {
	var content string
	var expected sql.NullString
	err := q.QueryRowContext(ctx, `SELECT m.content,j.read_source_sha256
 FROM workspace_conversation_agent_jobs j JOIN workspace_conversations c ON c.id=j.conversation_id
 JOIN workspace_conversation_messages m ON m.id=j.message_id AND m.conversation_id=c.id AND m.author_user_id=j.requested_by_user_id
 JOIN agents a ON a.id=j.agent_id
 WHERE j.id=? AND j.state IN ('pending','queued','completed') AND c.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM assignments cancelled WHERE cancelled.id=j.assignment_id AND cancelled.cancel_requested_at IS NOT NULL) AND EXISTS(SELECT 1 FROM workspaces live WHERE live.id=c.workspace_id AND live.deleted_at IS NULL)
 AND `+liveInboxJob, job).Scan(&content, &expected)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if expected.Valid && expected.String != InboxSourceDigest(content) {
		return ErrForbidden
	}
	return nil
}
