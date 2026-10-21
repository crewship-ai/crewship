package server

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// approvalOrigin projects only a durable direct-human source. A RunID may be
// minted before its origin exists; missing/legacy provenance keeps the old gate
// path and contributes no guessed chat. Inherited same-agent contexts are not
// proof that a delegated run is itself a human chat launch.
func (a *approvalGateAdapter) approvalOrigin(ctx context.Context, in orchestrator.ApprovalCheckInput) (map[string]any, *runauthority.Authority, error) {
	if in.Tool != "agent_run" {
		return in.Correlation, nil, nil
	}
	run, _ := in.Correlation["run_id"].(string)
	if run == "" {
		return nil, nil, nil
	}
	correlation := map[string]any{"run_id": run}
	chat, _ := in.Correlation["chat_id"].(string)
	trigger, _ := in.Correlation["trigger_message_id"].(string)
	if a.db == nil || chat == "" || trigger == "" {
		return correlation, nil, nil
	}
	var kind, human string
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.trigger_type'),''),COALESCE(json_type(payload,'$.human_authority'),'') FROM journal_entries WHERE trace_id=? AND workspace_id=? AND agent_id=? AND entry_type='run.started'`, run, in.WorkspaceID, in.AgentID).Scan(&kind, &human)
	if err != nil || kind != "USER" || human != "object" {
		return correlation, nil, nil
	}
	// Only the confidently recorded direct-human path has an admission fence.
	origin, err := runauthority.Resolve(ctx, a.db, run, in.AgentID, in.WorkspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("whole-run requester authority: %w", err)
	}
	if origin.HumanAuthority == nil || origin.ChatID != chat || origin.TriggerMessageID != trigger {
		return correlation, nil, nil
	}
	correlation["chat_id"] = origin.ChatID
	correlation["trigger_message_id"] = origin.TriggerMessageID
	correlation["trigger_type"] = "human_chat"
	return correlation, &origin, nil
}

func (a *approvalGateAdapter) recheckApprovalOrigin(ctx context.Context, origin *runauthority.Authority) error {
	if origin == nil {
		return nil
	}
	current, err := runauthority.Resolve(ctx, a.db, origin.RunID, origin.AgentID, origin.WorkspaceID)
	if err != nil {
		return fmt.Errorf("whole-run requester authority changed: %w", err)
	}
	if current.HumanAuthority == nil || *current.HumanAuthority != *origin.HumanAuthority || current.ChatID != origin.ChatID || current.TriggerMessageID != origin.TriggerMessageID {
		return fmt.Errorf("whole-run requester authority changed")
	}
	return nil
}
