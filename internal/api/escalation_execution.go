package api

import (
	"context"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Escalation asks a human for help; it does not authorize agent work. A live
// verified execution may ask even when its original requester grant is stale
// or unavailable. Delegation's stronger requester checks do not apply here.
func escalationExecutionChat(ctx context.Context, q issueOperationQueryer, run, agent, workspace string) (string, error) {
	var starts, ended int
	var recordedAgent, raw string
	err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(agent_id),''),COALESCE(MAX(payload),'') FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type='run.started'`, run, workspace).Scan(&starts, &recordedAgent, &raw)
	if err != nil {
		return "", err
	}
	if starts != 1 || recordedAgent != agent {
		return "", runauthority.ErrIdentityMismatch
	}
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, run, workspace).Scan(&ended); err != nil {
		return "", err
	}
	if ended != 0 {
		return "", runauthority.ErrIdentityMismatch
	}
	var payload struct {
		Chat string `json:"chat_id"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Chat == "" {
		return "", runauthority.ErrUnavailable
	}
	var valid bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats ch JOIN agents a ON a.workspace_id=ch.workspace_id AND a.deleted_at IS NULL JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL JOIN workspaces ws ON ws.id=ch.workspace_id AND ws.deleted_at IS NULL WHERE ch.id=? AND ch.workspace_id=? AND a.id=?)`, payload.Chat, workspace, agent).Scan(&valid)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", runauthority.ErrIdentityMismatch
	}
	return payload.Chat, nil
}
