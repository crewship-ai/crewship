package harbormaster

import (
	"context"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// ErrDecisionDenied applies only to the new host-correlated synchronous human
// whole-run source. Legacy, per-tool and nonblocking reviews keep their paths.
var ErrDecisionDenied = errors.New("whole-run approval access denied")

func guardHumanWholeRunDecision(ctx context.Context, q DBTX, row *Request, actor string) error {
	if row.Payload["tool"] != "agent_run" || row.Payload["gate_mode"] != "sync" || row.Kind == KindEphemeralHire || row.Kind == KindAutonomyGate {
		return nil
	}
	correlation, ok := row.Payload["correlation"].(map[string]any)
	if !ok || correlation["trigger_type"] != "human_chat" {
		return nil
	}
	run, _ := correlation["run_id"].(string)
	chat, _ := correlation["chat_id"].(string)
	trigger, _ := correlation["trigger_message_id"].(string)
	if run == "" || chat == "" || trigger == "" {
		return ErrDecisionDenied
	}
	if row.TimeoutAt != nil && !row.TimeoutAt.After(time.Now()) {
		return ErrNotPending
	}
	var allowed int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=? AND wm.role IN ('OWNER','ADMIN') AND wm.access_mode='trusted' AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')`, row.WorkspaceID, actor).Scan(&allowed)
	if err != nil {
		return err
	}
	if allowed != 1 {
		return ErrDecisionDenied
	}
	var direct int
	if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND trace_id=? AND agent_id=? AND entry_type='run.started' AND json_valid(payload) AND json_extract(payload,'$.trigger_type')='USER' AND json_type(payload,'$.human_authority')='object' AND COALESCE(json_extract(payload,'$.chat_origin_id'),'')=''`, row.WorkspaceID, run, row.AgentID).Scan(&direct); err != nil {
		return err
	}
	if direct != 1 {
		return ErrDecisionDenied
	}
	// Refusing work grants no execution authority. A currently authorized
	// operator must still be able to settle this gate after its requester loses
	// access; only approval needs a live requester execution receipt.
	if row.Status == StatusDenied {
		return nil
	}
	origin, err := runauthority.Resolve(ctx, q, run, row.AgentID, row.WorkspaceID)
	if err != nil || origin.TriggerType != "USER" || origin.HumanAuthority == nil || origin.ChatID != chat || origin.TriggerMessageID != trigger {
		return ErrDecisionDenied
	}
	return nil
}
