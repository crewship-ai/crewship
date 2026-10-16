package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Missing old or autonomous human provenance suppresses only the new card
// correlation, after the existing optional cryptographic run check succeeds.
func escalationHasHumanSource(ctx context.Context, q issueOperationQueryer, run, agent, workspace, chat string) (bool, error) {
	var raw, generation string
	if err := q.QueryRowContext(ctx, `SELECT payload FROM journal_entries WHERE workspace_id=? AND trace_id=? AND agent_id=? AND entry_type='run.started'`, workspace, run, agent).Scan(&raw); err != nil {
		return false, err
	}
	var source struct {
		ChatOrigin  string                `json:"chat_origin_id"`
		TriggerType string                `json:"trigger_type"`
		Chat        string                `json:"chat_id"`
		Trigger     string                `json:"trigger_message_id"`
		Human       *chataudience.Receipt `json:"human_authority"`
	}
	if json.Unmarshal([]byte(raw), &source) != nil {
		return false, runauthority.ErrIdentityMismatch
	}
	if source.TriggerType != "USER" {
		return false, nil
	}
	err := q.QueryRowContext(ctx, `SELECT authority_generation FROM chats WHERE id=? AND workspace_id=? AND agent_id=?`, chat, workspace, agent).Scan(&generation)
	if err == sql.ErrNoRows {
		return false, runauthority.ErrIdentityMismatch
	}
	if err != nil {
		return false, err
	}
	eligible := source.ChatOrigin == "" && source.Chat == chat && source.Trigger != "" && source.Human != nil && source.Human.ChatGeneration != "" && source.Human.ChatGeneration == generation && source.Human.WorkspaceID == workspace && source.Human.ChatID == chat && source.Human.AgentID == agent
	if !eligible {
		return false, runauthority.ErrIdentityMismatch
	}
	return true, nil
}
