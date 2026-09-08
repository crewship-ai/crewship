package groupchat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type AgentMember struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	JoinedAt    string `json:"joined_at"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	AvatarStyle string `json:"avatar_style,omitempty"`
	AvatarSeed  string `json:"avatar_seed,omitempty"`
}

func channelAgent(ctx context.Context, q querier, w, id, agentID string) error {
	var ok int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM workspace_conversation_agents ca JOIN agents a ON a.id=ca.agent_id LEFT JOIN crews cr ON cr.id=a.crew_id JOIN workspace_conversations c ON c.id=ca.conversation_id WHERE ca.conversation_id=? AND ca.agent_id=? AND a.workspace_id=? AND c.workspace_id=a.workspace_id AND a.deleted_at IS NULL AND c.kind='channel' AND c.deleted_at IS NULL`, id, agentID, w).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	return err
}
func (s *Store) Agents(ctx context.Context, w, u, id string) ([]AgentMember, error) {
	if _, err := s.Get(ctx, w, u, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,a.name,a.slug,ca.joined_at,`+agentIdentityCols+` FROM workspace_conversation_agents ca JOIN agents a ON a.id=ca.agent_id LEFT JOIN crews cr ON cr.id=a.crew_id JOIN workspace_conversations c ON c.id=ca.conversation_id WHERE c.id=? AND `+accessible+` AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL ORDER BY a.name,a.id`, id, w, u, u)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentMember{}
	for rows.Next() {
		var a AgentMember
		var identityJSON string
		if err := rows.Scan(&a.AgentID, &a.Name, &a.Slug, &a.JoinedAt, &identityJSON); err != nil {
			return nil, err
		}
		var identity agentIdentity
		if err := json.Unmarshal([]byte(identityJSON), &identity); err != nil {
			return nil, err
		}
		a.AvatarURL, a.AvatarStyle, a.AvatarSeed = identity.url(), identity.Style, identity.Seed
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddAgent emits the history-access event in the same transaction as membership.
// Only channels are supported: legacy assignment results are workspace-readable.
func (s *Store) AddAgent(ctx context.Context, w, u, id, agentID string) error {
	return s.changeAgent(ctx, w, u, id, agentID, true)
}
func (s *Store) RemoveAgent(ctx context.Context, w, u, id, agentID string) error {
	return s.changeAgent(ctx, w, u, id, agentID, false)
}
func (s *Store) changeAgent(ctx context.Context, w, u, id, agentID string, join bool) error {
	return s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if c.CreatedBy != u {
			return ErrForbidden
		}
		if c.Kind != "channel" {
			return ErrInvalid
		}
		var name string
		if err = q.QueryRowContext(ctx, `SELECT name FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, agentID, w).Scan(&name); errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		timestamp := now()
		var result sql.Result
		kind := "agent_joined"
		content := fmt.Sprintf("%s joined this channel and can receive its conversation history when mentioned.", name)
		if join {
			result, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_agents(conversation_id,agent_id,joined_by,joined_at) VALUES(?,?,?,?) ON CONFLICT(conversation_id,agent_id) DO NOTHING`, id, agentID, u, timestamp)
		} else {
			kind = "agent_left"
			content = fmt.Sprintf("%s was removed from this channel.", name)
			result, err = q.ExecContext(ctx, `DELETE FROM workspace_conversation_agents WHERE conversation_id=? AND agent_id=?`, id, agentID)
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if !join {
			if _, err = q.ExecContext(ctx, `UPDATE assignments SET cancel_requested_at=COALESCE(cancel_requested_at,?) WHERE id IN (SELECT assignment_id FROM workspace_conversation_agent_jobs WHERE conversation_id=? AND agent_id=? AND state IN ('pending','queued')) AND status IN ('PENDING','QUEUED','RUNNING')`, timestamp, id, agentID); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE workspace_conversation_agent_jobs SET state='failed',error='Agent removed from conversation',updated_at=? WHERE conversation_id=? AND agent_id=? AND state IN ('pending','queued')`, timestamp, id, agentID); err != nil {
				return err
			}
		}
		m := Message{ID: newID(), ConversationID: id, Sequence: c.LastSequence + 1, AuthorUserID: u, ClientID: newID(), Kind: kind, SubjectAgentID: agentID, Content: content, CreatedAt: timestamp}
		return appendEventMessage(ctx, q, m)
	})
}
func appendEventMessage(ctx context.Context, q querier, m Message) error {
	var human, agent any
	if m.AuthorUserID != "" {
		human = m.AuthorUserID
	}
	if m.AuthorAgentID != "" {
		agent = m.AuthorAgentID
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO workspace_conversation_messages(id,conversation_id,sequence,author_user_id,author_agent_id,client_id,content,created_at,kind,subject_agent_id) VALUES(?,?,?,?,?,?,?,?,?,?)`, m.ID, m.ConversationID, m.Sequence, human, agent, m.ClientID, m.Content, m.CreatedAt, m.Kind, m.SubjectAgentID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `UPDATE workspace_conversations SET last_sequence=?,updated_at=? WHERE id=?`, m.Sequence, m.CreatedAt, m.ConversationID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO workspace_conversation_outbox(id,conversation_id,message_id,created_at) VALUES(?,?,?,?)`, newID(), m.ConversationID, m.ID, m.CreatedAt)
	return err
}

// SendAgentReply is trusted-server-only. A durable job supplies identity, rather
// than an HTTP request. Completion and reply commit together and can be retried.
func (s *Store) SendAgentReply(ctx context.Context, jobID, content string) (Message, error) {
	if content == "" || len(content) > 262144 {
		return Message{}, ErrInvalid
	}
	var m Message
	err := s.write(ctx, func(q querier) error {
		var conversationID, agentID, state, replyID, w, requester string
		err := q.QueryRowContext(ctx, `SELECT j.conversation_id,COALESCE(j.agent_id,''),j.state,COALESCE(j.reply_message_id,''),c.workspace_id,COALESCE(j.requested_by_user_id,'') FROM workspace_conversation_agent_jobs j JOIN workspace_conversations c ON c.id=j.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE j.id=? AND c.deleted_at IS NULL AND w.deleted_at IS NULL`, jobID).Scan(&conversationID, &agentID, &state, &replyID, &w, &requester)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		if state == "completed" {
			m, err = scanMessage(q.QueryRowContext(ctx, `SELECT `+messageCols+` FROM workspace_conversation_messages m LEFT JOIN users u ON u.id=m.author_user_id WHERE m.id=?`, replyID))
			return err
		}
		if state != "queued" {
			return ErrConflict
		}
		if err = workspaceMember(ctx, q, w, requester); err != nil {
			return err
		}
		if err = channelAgent(ctx, q, w, conversationID, agentID); err != nil {
			return err
		}
		var activeCrew int
		if err = q.QueryRowContext(ctx, `SELECT 1 FROM agents a JOIN crews cr ON cr.id=a.crew_id WHERE a.id=? AND cr.workspace_id=? AND cr.deleted_at IS NULL`, agentID, w).Scan(&activeCrew); errors.Is(err, sql.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		var sequence int64
		if err = q.QueryRowContext(ctx, `SELECT last_sequence FROM workspace_conversations WHERE id=?`, conversationID).Scan(&sequence); err != nil {
			return err
		}
		m = Message{ID: newID(), ConversationID: conversationID, Sequence: sequence + 1, AuthorAgentID: agentID, Kind: "message", ClientID: "dispatch:" + jobID, Content: content, CreatedAt: now(), MentionedAgentIDs: []string{}}
		if err = q.QueryRowContext(ctx, `SELECT name FROM agents WHERE id=?`, agentID).Scan(&m.AuthorName); err != nil {
			return err
		}
		if err = appendEventMessage(ctx, q, m); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE workspace_conversation_agent_jobs SET state='completed',reply_message_id=?,updated_at=? WHERE id=?`, m.ID, m.CreatedAt, jobID)
		return err
	})
	return m, err
}

type Job struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	MessageID    string `json:"message_id"`
	State        string `json:"state"`
	Error        string `json:"error"`
	AssignmentID string `json:"assignment_id"`
}

func (s *Store) Jobs(ctx context.Context, w, u, id string) ([]Job, error) {
	if _, err := s.Get(ctx, w, u, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT j.id,COALESCE(j.agent_id,''),j.message_id,CASE WHEN j.state='queued' AND assignment.status='RUNNING' THEN 'running' ELSE j.state END,COALESCE(j.error,''),COALESCE(j.assignment_id,'') FROM workspace_conversation_agent_jobs j JOIN workspace_conversations c ON c.id=j.conversation_id LEFT JOIN assignments assignment ON assignment.id=j.assignment_id WHERE c.id=? AND `+accessible+` ORDER BY j.created_at DESC,j.id DESC LIMIT 100`, id, w, u, u)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.AgentID, &j.MessageID, &j.State, &j.Error, &j.AssignmentID); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
