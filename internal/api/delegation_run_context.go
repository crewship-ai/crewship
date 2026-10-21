package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

var errDelegationExecutionChatUnavailable = fmt.Errorf("delegation execution chat unavailable: %w", runauthority.ErrUnavailable)

// A delegated run's execution chat can differ from its root human chat. Resolve
// first verifies identity/authority; only the durable host start names the chat
// where this execution's assignment side effects belong.
func delegationExecutionChat(ctx context.Context, q issueOperationQueryer, runID, agentID, workspaceID string) (string, error) {
	if _, err := runauthority.Resolve(ctx, q, runID, agentID, workspaceID); err != nil {
		return "", err
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT payload FROM journal_entries WHERE trace_id=? AND workspace_id=? AND agent_id=? AND entry_type='run.started'`, runID, workspaceID, agentID).Scan(&raw); err != nil {
		return "", err
	}
	var payload struct {
		ChatID string `json:"chat_id"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.ChatID == "" {
		return "", errDelegationExecutionChatUnavailable
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chats WHERE id=? AND workspace_id=?)`, payload.ChatID, workspaceID).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", runauthority.ErrIdentityMismatch
	}
	return payload.ChatID, nil
}
