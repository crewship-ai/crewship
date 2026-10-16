package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type runApprovalSource struct {
	Row          *harbormaster.Request
	Run, Trigger string
	Requester    chataudience.Receipt
}

// Host correlation is necessary but not sufficient: bind it to this run's
// unique durable direct-human start, never an inherited authority genealogy.
func readRunApprovalSource(ctx context.Context, q harbormaster.DBTX, a chataudience.Receipt, id string) (runApprovalSource, error) {
	var s runApprovalSource
	row, err := harbormaster.GetTx(ctx, q, a.WorkspaceID, id)
	if err != nil {
		return s, err
	}
	if row == nil {
		return s, sql.ErrNoRows
	}
	if row.AgentID != a.AgentID || row.Payload["tool"] != "agent_run" || row.Payload["gate_mode"] != "sync" || row.Kind == harbormaster.KindEphemeralHire || row.Kind == harbormaster.KindAutonomyGate {
		return s, sql.ErrNoRows
	}
	correlation, ok := row.Payload["correlation"].(map[string]any)
	if !ok || correlation["chat_id"] != a.ChatID || correlation["trigger_type"] != "human_chat" {
		return s, sql.ErrNoRows
	}
	s.Run, _ = correlation["run_id"].(string)
	s.Trigger, _ = correlation["trigger_message_id"].(string)
	if !cardText(s.Run, 1, 200) || !cardText(s.Trigger, 1, 200) {
		return s, sql.ErrNoRows
	}
	var count int
	var raw, recordedAgent string
	err = q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(payload),''),COALESCE(MAX(agent_id),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type='run.started'`, a.WorkspaceID, s.Run).Scan(&count, &raw, &recordedAgent)
	if err != nil {
		return s, err
	}
	var origin struct {
		Chat      string                `json:"chat_id"`
		Trigger   string                `json:"trigger_message_id"`
		Type      string                `json:"trigger_type"`
		Inherited string                `json:"chat_origin_id"`
		Human     *chataudience.Receipt `json:"human_authority"`
	}
	if count != 1 || recordedAgent != a.AgentID || json.Unmarshal([]byte(raw), &origin) != nil || origin.Type != "USER" || origin.Inherited != "" || origin.Chat != a.ChatID || origin.Trigger != s.Trigger || origin.Human == nil {
		return s, sql.ErrNoRows
	}
	s.Requester = *origin.Human
	if s.Requester.ChatID != a.ChatID || s.Requester.WorkspaceID != a.WorkspaceID || s.Requester.AgentID != a.AgentID {
		return s, sql.ErrNoRows
	}
	s.Row = row
	return s, nil
}
func (s runApprovalSource) version() string {
	raw, _ := json.Marshal(s.Row)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r *Router) runApprovalDecision(ctx context.Context, a chataudience.Receipt, id string) (ChatCardProjection, error) {
	live, err := chataudience.CaptureTrustedQuery(ctx, r.db, a.ChatID, a.UserID, &a)
	if err != nil {
		return ChatCardProjection{}, err
	}
	if live == nil {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	s, err := readRunApprovalSource(ctx, r.db, a, id)
	if err != nil {
		return ChatCardProjection{}, err
	}
	var role string
	if err = r.db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=? AND access_mode='trusted'`, a.MemberID).Scan(&role); err != nil {
		return ChatCardProjection{}, err
	}
	row := s.Row
	d := chatentities.DecisionCard{SchemaVersion: 1, Source: chatentities.DecisionSource{Type: "run_approval", ID: row.ID, Version: s.version()}, DecisionType: "whole_run_approval", Status: "pending", Title: truncate(inbox.RedactSecrets(row.Reason), 240), Fields: []chatentities.DecisionField{}, AllowedActions: []string{"view"}}
	if row.TimeoutAt != nil {
		value := tsformat.Format(*row.TimeoutAt)
		d.ExpiresAt = &value
	}
	switch row.Status {
	case harbormaster.StatusPending:
		origin, e := runauthority.Resolve(ctx, r.db, s.Run, a.AgentID, a.WorkspaceID)
		if e != nil && !errors.Is(e, runauthority.ErrUnavailable) && !errors.Is(e, runauthority.ErrIdentityMismatch) && !errors.Is(e, runauthority.ErrRequesterStale) {
			return ChatCardProjection{}, e
		}
		requesterLive := e == nil && origin.HumanAuthority != nil && *origin.HumanAuthority == s.Requester && origin.TriggerType == "USER"
		if row.TimeoutAt != nil && !row.TimeoutAt.After(time.Now()) {
			d.Status = "expired"
		} else if (role == "OWNER" || role == "ADMIN") && canScope(ctx, "workspace:admin") {
			if requesterLive {
				d.AllowedActions = append(d.AllowedActions, "approve")
			}
			d.AllowedActions = append(d.AllowedActions, "reject")
		}
	case harbormaster.StatusApproved, harbormaster.StatusDenied:
		if row.DecidedBy == "" || row.DecidedAt == nil {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		var human int
		if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, row.DecidedBy).Scan(&human); err != nil {
			return ChatCardProjection{}, err
		}
		if human != 1 {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		d.Status = "approved"
		if row.Status == harbormaster.StatusDenied {
			d.Status = "rejected"
		}
		d.Resolution = &chatentities.DecisionResolution{AnsweredBy: row.DecidedBy, AnsweredAt: tsformat.Format(*row.DecidedAt), Values: map[string]json.RawMessage{}}
	case harbormaster.StatusTimeout:
		d.Status = "expired"
	case harbormaster.StatusCancelled:
		d.Status = "cancelled"
	default:
		return ChatCardProjection{}, sql.ErrNoRows
	}
	ref := ChatCardResponse{EntityRefResponse: EntityRefResponse{EventID: "decision:" + row.ID, Kind: "decision", Verb: "asked", Entity: EntityRefEntity{ID: row.ID, WorkspaceID: row.WorkspaceID}, Provenance: EntityRefProvenance{Actor: EntityRefActor{Type: "agent", ID: row.AgentID}, At: tsformat.Format(row.CreatedAt)}, Anchor: EntityRefAnchor{RunID: s.Run, TriggerMessageID: s.Trigger}}, Decision: &d}
	if err = validateE2Card(ref); err != nil {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	return ChatCardProjection{ChatID: a.ChatID, Ref: ref}, nil
}

// Candidates reveal no existence themselves. The common pager counts only
// successfully authorized Read projections and closes Scan rows before Read.
func (r *Router) runApprovalUpper(ctx context.Context, a chataudience.Receipt) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM approvals_queue WHERE workspace_id=? AND agent_id=?`, a.WorkspaceID, a.AgentID).Scan(&n)
	return n, err
}
func (r *Router) runApprovalScan(ctx context.Context, a chataudience.Receipt, after, upper int64, limit int) ([]decisionSourceRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,rowid FROM approvals_queue WHERE workspace_id=? AND agent_id=? AND rowid>? AND rowid<=? ORDER BY rowid LIMIT ?`, a.WorkspaceID, a.AgentID, after, upper, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []decisionSourceRow{}
	for rows.Next() {
		var s decisionSourceRow
		if err = rows.Scan(&s.ID, &s.Position); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
