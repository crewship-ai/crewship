// Package runauthority resolves server-recorded chat-run provenance. Agent
// request fields never substitute for the human admission receipt.
package runauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

var (
	ErrUnavailable      = errors.New("run_authority_unavailable")
	ErrIdentityMismatch = errors.New("run_identity_mismatch")
	ErrRequesterStale   = errors.New("requester_authority_stale")
)

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Authority struct {
	RunID            string
	TriggerType      string
	AgentID          string
	WorkspaceID      string
	ChatID           string
	TriggerMessageID string
	HumanAuthority   *chataudience.Receipt
	ChatOriginID     string
}

type triggerKey struct{}

// WithTriggerMessage is host-internal provenance, never socket metadata.
func WithTriggerMessage(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, triggerKey{}, id)
}
func TriggerMessageFromContext(ctx context.Context) string {
	id, _ := ctx.Value(triggerKey{}).(string)
	return id
}

// Resolve must run in the mutation transaction when its result authorizes a
// write. Both a DB and a caller-owned transaction implement Queryer.
func Resolve(ctx context.Context, q Queryer, runID, agentID, workspaceID string) (Authority, error) {
	a := Authority{RunID: runID, AgentID: agentID, WorkspaceID: workspaceID}
	if q == nil || runID == "" || agentID == "" || workspaceID == "" {
		return a, ErrUnavailable
	}
	var recordedAgent, recordedWorkspace, raw, actor string
	var starts, ended int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(agent_id),''), COALESCE(MAX(workspace_id),''), COALESCE(MAX(payload),''), COALESCE(MAX(actor_type),'') FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type='run.started'`, runID, workspaceID).Scan(&starts, &recordedAgent, &recordedWorkspace, &raw, &actor)
	if err != nil {
		return a, fmt.Errorf("resolve run start: %w", err)
	}
	if starts != 1 {
		return a, ErrUnavailable
	}
	if recordedAgent != agentID || recordedWorkspace != workspaceID {
		return a, ErrIdentityMismatch
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND workspace_id=? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop')`, runID, workspaceID).Scan(&ended); err != nil {
		return a, fmt.Errorf("resolve run terminal: %w", err)
	}
	if ended != 0 {
		return a, ErrIdentityMismatch
	}
	var payload struct {
		ChatOriginID     string                `json:"chat_origin_id"`
		TriggerType      string                `json:"trigger_type"`
		HostAdmitted     bool                  `json:"host_admitted"`
		ChatID           string                `json:"chat_id"`
		TriggerMessageID string                `json:"trigger_message_id"`
		HumanAuthority   *chataudience.Receipt `json:"human_authority"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return a, ErrUnavailable
	}
	a.TriggerType = payload.TriggerType
	if payload.ChatOriginID != "" {
		if (!payload.HostAdmitted && actor != "orchestrator") || payload.HumanAuthority != nil {
			return a, ErrIdentityMismatch
		}
		inherited, err := ValidateOrigin(ctx, q, payload.ChatOriginID, agentID, workspaceID)
		if err != nil {
			return a, err
		}
		inherited.RunID, inherited.TriggerType = runID, payload.TriggerType
		return inherited, nil
	}
	if payload.HumanAuthority == nil {
		switch payload.TriggerType {
		case "SCHEDULED", "ASSIGNMENT", "WEBHOOK", "ROUTINE", "CRON", "PEER_QUERY":
			if payload.HostAdmitted {
				a.ChatID = payload.ChatID
				return a, nil
			}
		}
		return a, ErrUnavailable
	}
	if payload.ChatID == "" || payload.TriggerMessageID == "" {
		return a, ErrUnavailable
	}
	r := payload.HumanAuthority
	if r.AgentID != agentID || r.WorkspaceID != workspaceID || r.ChatID != payload.ChatID {
		return a, ErrIdentityMismatch
	}
	live, err := chataudience.CaptureTrustedQuery(ctx, q, r.ChatID, r.UserID, r)
	if err != nil {
		return a, err
	}
	if live == nil {
		return a, ErrRequesterStale
	}
	a.ChatID, a.TriggerMessageID, a.HumanAuthority = payload.ChatID, payload.TriggerMessageID, live
	return a, nil
}
