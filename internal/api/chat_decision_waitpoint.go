package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

type waitpointDecisionSource struct {
	ID, Workspace, PipelineRun, Run, Agent, Prompt, Created, Deadline, Status string
	Form, Payload, Actor, Resolved, OperationKey, OperationHash               string
}

func readWaitpointDecision(ctx context.Context, q issueOperationQueryer, workspace, id string) (waitpointDecisionSource, error) {
	var s waitpointDecisionSource
	err := q.QueryRowContext(ctx, `SELECT w.token,w.workspace_id,w.pipeline_run_id,COALESCE(p.invocation_source_run_id,''),COALESCE(p.invoking_agent_id,''),w.prompt,w.created_at,w.timeout_at,w.status,w.decision_form_json,COALESCE(w.decision_payload,''),COALESCE(w.decided_by_user_id,''),COALESCE(w.decided_at,''),COALESCE(w.answer_operation_key,''),COALESCE(w.answer_operation_hash,'') FROM pipeline_waitpoints w JOIN pipeline_runs p ON p.id=w.pipeline_run_id AND p.workspace_id=w.workspace_id WHERE w.workspace_id=? AND w.token=? AND w.kind='approval'`, workspace, id).Scan(&s.ID, &s.Workspace, &s.PipelineRun, &s.Run, &s.Agent, &s.Prompt, &s.Created, &s.Deadline, &s.Status, &s.Form, &s.Payload, &s.Actor, &s.Resolved, &s.OperationKey, &s.OperationHash)
	return s, err
}

func (s waitpointDecisionSource) version() string {
	// The receipt authenticates retries; it is not a second source revision.
	s.OperationKey, s.OperationHash = "", ""
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Keep source semantics representable without widening the frozen card schema.
// Unsupported legacy forms remain fully usable on the existing Inbox surface.
func waitpointDecisionForm(raw string) ([]chatentities.DecisionField, []chatentities.DecisionWorkflowAction, error) {
	fields := []chatentities.DecisionField{}
	if raw == "" {
		return fields, nil, nil
	}
	var form pipeline.DecisionForm
	if json.Unmarshal([]byte(raw), &form) != nil || pipeline.ValidateDecisionForm(&form) != nil || len(form.Fields) > 20 {
		return nil, nil, sql.ErrNoRows
	}
	for _, input := range form.Fields {
		if inbox.RedactSecrets(input.Name) != input.Name || input.Default != nil || input.AllowCustom || input.Type == "array" || input.Type == "object" {
			return nil, nil, sql.ErrNoRows
		}
		kind := map[string]string{"string": "text", "integer": "number", "number": "number", "boolean": "boolean"}[input.Type]
		if kind == "" {
			return nil, nil, sql.ErrNoRows
		}
		if input.Widget == "textarea" {
			kind = "textarea"
		}
		field := chatentities.DecisionField{ID: input.Name, Type: kind, Label: inbox.RedactSecrets(input.Label), Required: input.Required}
		if strings.TrimSpace(field.Label) == "" {
			field.Label = input.Name
		}
		if len(input.Options) > 0 {
			field.Type = "choice"
			for _, option := range input.Options {
				// Redacting a canonical option would silently change its meaning.
				if inbox.RedactSecrets(option) != option {
					return nil, nil, sql.ErrNoRows
				}
				field.Options = append(field.Options, chatentities.DecisionOption{Value: option, Label: option})
			}
		}
		fields = append(fields, field)
	}
	actions := make([]chatentities.DecisionWorkflowAction, 0, len(form.Actions))
	for _, action := range form.Actions {
		if inbox.RedactSecrets(action.ID) != action.ID {
			return nil, nil, sql.ErrNoRows
		}
		actions = append(actions, chatentities.DecisionWorkflowAction{ID: action.ID, Label: inbox.RedactSecrets(action.Label), Approved: action.Approved})
	}
	return fields, actions, nil
}

func waitpointDecisionProjection(ctx context.Context, q issueOperationQueryer, a chataudience.Receipt, s waitpointDecisionSource) (ChatCardProjection, error) {
	if s.Run == "" || s.Agent != a.AgentID {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	var count int
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(payload),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND agent_id=? AND entry_type='run.started'`, a.WorkspaceID, s.Run, a.AgentID).Scan(&count, &raw); err != nil {
		return ChatCardProjection{}, err
	}
	var proof struct {
		Chat        string                `json:"chat_id"`
		Trigger     string                `json:"trigger_message_id"`
		TriggerType string                `json:"trigger_type"`
		ChatOrigin  string                `json:"chat_origin_id"`
		Human       *chataudience.Receipt `json:"human_authority"`
	}
	if count != 1 || json.Unmarshal([]byte(raw), &proof) != nil {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	if proof.Chat != a.ChatID || proof.Trigger == "" || proof.TriggerType != "USER" || proof.ChatOrigin != "" || proof.Human == nil || proof.Human.WorkspaceID != a.WorkspaceID || proof.Human.ChatID != a.ChatID || proof.Human.AgentID != a.AgentID {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	fields, actions, err := waitpointDecisionForm(s.Form)
	if err != nil {
		return ChatCardProjection{}, err
	}
	var role string
	if err = q.QueryRowContext(ctx, `SELECT wm.role FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.id=? AND wm.workspace_id=? AND wm.user_id=? AND wm.access_mode='trusted'`, a.MemberID, a.WorkspaceID, a.UserID).Scan(&role); err != nil {
		return ChatCardProjection{}, err
	}
	if !canRole(role, "read") {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	d := chatentities.DecisionCard{SchemaVersion: 1, Source: chatentities.DecisionSource{Type: "waitpoint", ID: s.ID, Version: s.version()}, DecisionType: "workflow", Status: "pending", Title: truncate(inbox.RedactSecrets(s.Prompt), 240), Fields: fields, WorkflowActions: actions, ExpiresAt: &s.Deadline, AllowedActions: []string{"view"}}
	switch s.Status {
	case "pending":
		deadline, err := time.Parse(time.RFC3339Nano, s.Deadline)
		if err != nil {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		if !deadline.After(time.Now()) {
			d.Status = "expired"
		} else if canRole(role, "update") && canScope(ctx, "workspace:admin") {
			if len(actions) > 0 {
				d.AllowedActions = append(d.AllowedActions, "answer")
			} else {
				d.AllowedActions = append(d.AllowedActions, "approve", "reject")
			}
		}
	case "timed_out":
		d.Status = "expired"
	case "cancelled":
		d.Status = "cancelled"
	case "approved", "denied":
		var human int
		if err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, s.Actor).Scan(&human); err != nil {
			return ChatCardProjection{}, err
		}
		if human != 1 {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		d.Status = "approved"
		if s.Status == "denied" {
			d.Status = "rejected"
		}
		resolution := &chatentities.DecisionResolution{AnsweredBy: s.Actor, AnsweredAt: s.Resolved, Values: map[string]json.RawMessage{}}
		if s.Form != "" {
			var answer struct {
				ActionID string                     `json:"action_id"`
				Data     map[string]json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(s.Payload), &answer) != nil || answer.Data == nil {
				return ChatCardProjection{}, sql.ErrNoRows
			}
			resolution.ActionID, resolution.Values = answer.ActionID, answer.Data
			for key, value := range resolution.Values {
				// Canonical Inbox permits optional null; the closed card uses
				// omission for no value without rewriting the durable payload.
				if strings.TrimSpace(string(value)) == "null" {
					for _, field := range fields {
						if field.ID == key && (!field.Required || d.Status == "rejected") {
							delete(resolution.Values, key)
						}
					}
					continue
				}
				var text string
				if json.Unmarshal(value, &text) == nil {
					resolution.Values[key] = decisionString(inbox.RedactSecrets(text))
				}
			}
		}
		d.Resolution = resolution
	default:
		return ChatCardProjection{}, sql.ErrNoRows
	}
	ref := ChatCardResponse{EntityRefResponse: EntityRefResponse{EventID: "decision:waitpoint:" + s.ID, Kind: "decision", Verb: "asked", Entity: EntityRefEntity{ID: s.ID, WorkspaceID: s.Workspace}, Provenance: EntityRefProvenance{Actor: EntityRefActor{Type: "agent", ID: a.AgentID}, At: s.Created}, Anchor: EntityRefAnchor{RunID: s.Run, TriggerMessageID: proof.Trigger}}, Decision: &d}
	if validateE2Card(ref) != nil {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	return ChatCardProjection{ChatID: a.ChatID, Ref: ref}, nil
}

func (r *Router) waitpointDecision(ctx context.Context, a chataudience.Receipt, id string) (ChatCardProjection, error) {
	live, err := chataudience.CaptureTrustedQuery(ctx, r.db, a.ChatID, a.UserID, &a)
	if err != nil {
		return ChatCardProjection{}, err
	}
	if live == nil {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	s, err := readWaitpointDecision(ctx, r.db, a.WorkspaceID, id)
	if err != nil {
		return ChatCardProjection{}, err
	}
	return waitpointDecisionProjection(ctx, r.db, a, s)
}
