package backup

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

type legacyOriginEvidence struct {
	root, chat, message, receipt string
	autonomous                   bool
}

// Historical evidence never renews an epoch. It is safe to retain an ended
// human parent's exact receipt because execution still validates it live.
func readLegacyOriginEvidence(ctx context.Context, tx *sql.Tx, workspace, root string) (legacyOriginEvidence, error) {
	seen := map[string]bool{}
	for depth := 0; root != "" && depth <= 16 && !seen[root]; depth++ {
		seen[root] = true
		var count int
		var raw, actor, agent string
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(payload),''),COALESCE(MAX(actor_type),''),COALESCE(MAX(agent_id),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type='run.started'`, workspace, root).Scan(&count, &raw, &actor, &agent); err != nil {
			return legacyOriginEvidence{}, err
		}
		if count > 0 {
			if count != 1 {
				return legacyOriginEvidence{}, nil
			}
			var p struct {
				HostAdmitted              bool            `json:"host_admitted"`
				TriggerType               string          `json:"trigger_type"`
				ChatOriginID              string          `json:"chat_origin_id"`
				ChatID                    string          `json:"chat_id"`
				TriggerMessageID          string          `json:"trigger_message_id"`
				HumanAuthority            json.RawMessage `json:"human_authority"`
				RequesterAuthorityMissing json.RawMessage `json:"requester_authority_missing"`
			}
			if json.Unmarshal([]byte(raw), &p) != nil {
				return legacyOriginEvidence{}, nil
			}
			if (len(p.RequesterAuthorityMissing) > 0 && string(p.RequesterAuthorityMissing) != "false") || string(p.HumanAuthority) == "null" {
				return legacyOriginEvidence{}, nil
			}
			if len(p.HumanAuthority) > 0 {
				var receipt chataudience.Receipt
				if (actor != "orchestrator" && !p.HostAdmitted) || p.ChatOriginID != "" || json.Unmarshal(p.HumanAuthority, &receipt) != nil || receipt.UserID == "" || receipt.AgentID == "" || receipt.AgentID != agent || receipt.MemberID == "" || receipt.ChatGeneration == "" || receipt.MemberRevision < 1 || receipt.ChatRevision < 1 || receipt.WorkspaceID != workspace || receipt.ChatID != p.ChatID || p.ChatID == "" || p.TriggerMessageID == "" {
					return legacyOriginEvidence{}, nil
				}
				return legacyOriginEvidence{root: root, chat: p.ChatID, message: p.TriggerMessageID, receipt: string(p.HumanAuthority)}, nil
			}
			if p.HostAdmitted && p.ChatOriginID == "" {
				switch p.TriggerType {
				case "SCHEDULED", "ASSIGNMENT", "WEBHOOK", "ROUTINE", "CRON", "PEER_QUERY":
					return legacyOriginEvidence{autonomous: true}, nil
				}
			}
			return legacyOriginEvidence{}, nil
		}
		var parent string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(chain_origin,''),NULLIF(triggered_by_id,''),'') FROM pipeline_runs WHERE id=? AND workspace_id=?`, root, workspace).Scan(&parent)
		if err == sql.ErrNoRows {
			return legacyOriginEvidence{}, nil
		}
		if err != nil {
			return legacyOriginEvidence{}, err
		}
		root = parent
	}
	return legacyOriginEvidence{}, nil
}
