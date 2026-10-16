package api

import (
	"context"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// pipelineInvocationSource projects only this actual direct-human invocation.
// A recipe's inherited execution origin is authority, not a new chat question.
// The caller has already verified the optional run credential and existing
// CaptureOrigin admission. Missing legacy metadata affects projection only.
func pipelineInvocationSource(ctx context.Context, q runauthority.Queryer, run, agent, workspace string) (string, error) {
	var count int
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(payload),'') FROM journal_entries WHERE trace_id=? AND agent_id=? AND workspace_id=? AND entry_type='run.started'`, run, agent, workspace).Scan(&count, &raw); err != nil {
		return "", err
	}
	var source struct {
		TriggerType      string                `json:"trigger_type"`
		ChatOriginID     string                `json:"chat_origin_id"`
		ChatID           string                `json:"chat_id"`
		TriggerMessageID string                `json:"trigger_message_id"`
		HumanAuthority   *chataudience.Receipt `json:"human_authority"`
	}
	if count != 1 || json.Unmarshal([]byte(raw), &source) != nil || source.TriggerType != "USER" || source.ChatOriginID != "" || source.HumanAuthority == nil || source.ChatID == "" || source.TriggerMessageID == "" {
		return "", nil
	}
	receipt := source.HumanAuthority
	if receipt.WorkspaceID != workspace || receipt.AgentID != agent || receipt.ChatID != source.ChatID {
		return "", nil
	}
	live, err := chataudience.CaptureTrustedQuery(ctx, q, source.ChatID, receipt.UserID, receipt)
	if err != nil {
		return "", err
	}
	if live == nil {
		return "", nil
	}
	return run, nil
}
