package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

var errProposalVersion = errors.New("proposal_version_conflict")
var errProposalForbidden = errors.New("proposal_action_forbidden")

// A proposal is inert intent. Its immutable source is captured from the
// authenticated sidecar's run; accepting it uses only the current human.
type storedProposal struct {
	ID, Workspace, Chat, Agent, Run, Trigger, TriggerType, Operation, Event, Created string
	Card                                                                             chatentities.ProposalCard
}

func loadProposal(ctx context.Context, q chatentities.Queryer, chat, id string) (storedProposal, error) {
	var p storedProposal
	var items string
	var outcome sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,workspace_id,chat_id,agent_id,run_id,trigger_message_id,trigger_type,operation_id,event_id,created_at,version,status,title,items,expires_at,outcome FROM chat_issue_proposals WHERE chat_id=? AND id=?`, chat, id).Scan(&p.ID, &p.Workspace, &p.Chat, &p.Agent, &p.Run, &p.Trigger, &p.TriggerType, &p.Operation, &p.Event, &p.Created, &p.Card.Version, &p.Card.Status, &p.Card.Title, &items, &p.Card.ExpiresAt, &outcome)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(items), &p.Card.Items); err != nil {
		return p, err
	}
	if outcome.Valid {
		if err = json.Unmarshal([]byte(outcome.String), &p.Card.Outcome); err != nil {
			return p, err
		}
	}
	p.Card.SchemaVersion = 1
	p.Card.AllowedActions = []string{"view"}
	if p.Card.Status == "draft" {
		expiry, e := time.Parse(time.RFC3339Nano, p.Card.ExpiresAt)
		if e != nil {
			return p, e
		}
		if !expiry.After(time.Now()) {
			p.Card.Status = "expired"
		}
	}
	return p, nil
}

func (p storedProposal) response() ChatCardResponse {
	return ChatCardResponse{EntityRefResponse: EntityRefResponse{EventID: p.Event, OperationID: p.Operation, Kind: "proposal", Verb: "proposal.create", Entity: EntityRefEntity{ID: p.ID, WorkspaceID: p.Workspace}, Provenance: EntityRefProvenance{Actor: EntityRefActor{Type: "agent", ID: p.Agent}, At: p.Created}, Anchor: EntityRefAnchor{RunID: p.Run, TriggerMessageID: p.Trigger}, RevisionAtAction: EntityRefRevision{Revision: 1}}, Proposal: &p.Card}
}

// Resolve ambiguous IDs fail closed. The public contract intentionally carries
// no model-chosen actor type; current workspace metadata decides that type.
func proposalAssignee(ctx context.Context, q chatentities.Queryer, workspace string, id *string) (*string, error) {
	if id == nil {
		return nil, nil
	}
	var users, agents int
	err := q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM workspace_members wm JOIN users u ON u.id=wm.user_id WHERE wm.workspace_id=? AND wm.user_id=? AND u.suspended_at IS NULL), (SELECT COUNT(*) FROM agents WHERE workspace_id=? AND id=? AND deleted_at IS NULL)`, workspace, *id, workspace, *id).Scan(&users, &agents)
	if err != nil {
		return nil, err
	}
	if users+agents != 1 {
		return nil, sql.ErrNoRows
	}
	actor := "user"
	if agents == 1 {
		actor = "agent"
	}
	return &actor, nil
}

// Trusted members use the same workspace-wide object scope as the existing
// issue surface. Restricted/guest membership never exposes E2 objects.
func proposalItemsVisible(ctx context.Context, q chatentities.Queryer, p storedProposal) error {
	for _, item := range p.Card.Items {
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, item.CrewID, p.Workspace).Scan(&one)
		if err != nil {
			return err
		}
		if item.ProjectID != nil {
			if err = q.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=? AND workspace_id=?`, *item.ProjectID, p.Workspace).Scan(&one); err != nil {
				return err
			}
		}
		if _, err = proposalAssignee(ctx, q, p.Workspace, item.AssigneeID); err != nil {
			return err
		}
	}
	return nil
}

func proposalHuman(ctx context.Context, tx *sql.Tx, a chataudience.Receipt, write bool) error {
	current, err := chataudience.CaptureTrustedQuery(ctx, tx, a.ChatID, a.UserID, nil)
	if err != nil {
		return err
	}
	if current == nil || current.WorkspaceID != a.WorkspaceID {
		return sql.ErrNoRows
	}
	var role string
	var caps sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT role,capabilities FROM workspace_members WHERE id=? AND access_mode='trusted' AND role <> 'GUEST'`, current.MemberID).Scan(&role, &caps); err != nil {
		return err
	}
	if write && !canRole(role, "create") && !HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityIssueCreate) {
		return errProposalForbidden
	}
	return nil
}

func proposalHash(verb, chat, id string, body any) string {
	raw, _ := json.Marshal([]any{verb, chat, id, body})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Creation is called only after sidecar authentication. No chat/run/actor fields
// exist in ProposalCreate, so model arguments cannot redirect provenance.
func (r *Router) createProposal(ctx context.Context, run, agent, workspace, key string, body chatentities.ProposalCreate) (ChatCardResponse, error) {
	var zero ChatCardResponse
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE chat_issue_proposals SET version=version WHERE id='' AND status='draft'`); err != nil {
		return zero, err
	}
	if err = proposalAgentTokenScope(ctx, tx, workspace, agent); err != nil {
		return zero, err
	}
	a, err := runauthority.Resolve(ctx, tx, run, agent, workspace)
	if err != nil {
		return zero, err
	}
	if a.ChatID == "" || a.TriggerMessageID == "" || a.HumanAuthority == nil {
		return zero, errProposalForbidden
	}
	card := chatentities.ProposalCard{SchemaVersion: 1, Version: 1, Status: "draft", Title: body.Title, Items: body.Items, ExpiresAt: tsformat.Format(time.Now().Add(24 * time.Hour)), AllowedActions: []string{"view"}}
	if err = validateProposalCard(card); err != nil {
		return zero, err
	}
	p := storedProposal{ID: generateCUID(), Workspace: workspace, Chat: a.ChatID, Agent: agent, Run: run, Trigger: a.TriggerMessageID, TriggerType: a.TriggerType, Event: generateCUID(), Created: tsformat.Format(time.Now()), Card: card}
	if err = proposalItemsVisible(ctx, tx, p); err != nil {
		return zero, err
	}
	for _, item := range p.Card.Items {
		var valid int
		if err = tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND crew_id=? AND deleted_at IS NULL`, agent, workspace, item.CrewID).Scan(&valid); err != nil {
			return zero, err
		}
	}
	op, replay, err := beginProposalReceipt(ctx, tx, workspace, "agent", agent, key, proposalHash("create", a.ChatID, "", body), "create", run, p.ID)
	if err != nil {
		return zero, err
	}
	if replay {
		old, e := loadProposal(ctx, tx, a.ChatID, op.ProposalID)
		if e != nil {
			return zero, e
		}
		if e = proposalAgentItemsVisible(ctx, tx, old, agent); e != nil {
			return zero, e
		}
		return old.response(), nil
	}
	p.Operation = op.ID
	items, _ := json.Marshal(card.Items)
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_issue_proposals(id,workspace_id,chat_id,agent_id,run_id,trigger_message_id,trigger_type,title,items,expires_at,operation_id,event_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ID, workspace, p.Chat, agent, run, p.Trigger, p.TriggerType, body.Title, string(items), card.ExpiresAt, op.ID, p.Event, p.Created)
	if err != nil {
		return zero, err
	}
	result := p.response()
	raw, _ := json.Marshal(result)
	if err = completeProposalReceipt(ctx, tx, op, raw); err != nil {
		return zero, err
	}
	return result, tx.Commit()
}

func (r *Router) mutateProposal(req *http.Request, a chataudience.Receipt, id, key, verb string, version int64, body any) (ChatCardResponse, error) {
	ctx := req.Context()
	var zero ChatCardResponse
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	// Reserve the SQLite writer before reading authority/version. This serializes
	// concurrent confirmers without a persistent intermediate proposal state.
	if _, err = tx.ExecContext(ctx, `UPDATE chat_issue_proposals SET version=version WHERE id=? AND chat_id=? AND status='draft'`, id, a.ChatID); err != nil {
		return zero, err
	}
	if err = proposalHuman(ctx, tx, a, false); err != nil {
		return zero, err
	}
	p, err := loadProposal(ctx, tx, a.ChatID, id)
	if err != nil {
		return zero, err
	}
	if p.Workspace != a.WorkspaceID {
		return zero, sql.ErrNoRows
	}
	if err = proposalItemsVisible(ctx, tx, p); err != nil {
		return zero, err
	}
	op, replay, err := beginProposalReceipt(ctx, tx, a.WorkspaceID, "human", a.UserID, key, proposalHash(verb, a.ChatID, id, body), verb, "", id)
	if err != nil {
		return zero, err
	}
	if replay {
		var result ChatCardResponse
		if err = json.Unmarshal(op.Result, &result); err != nil {
			return zero, err
		}
		// Old edit payloads may reference objects removed since a later revision.
		// Project the current authorized source instead of resurrecting that payload.
		if verb == "edit" && (p.Card.Status != "draft" || result.Proposal == nil || p.Card.Version != result.Proposal.Version) {
			return p.response(), nil
		}
		return result, nil
	}
	if err = proposalHuman(ctx, tx, a, true); err != nil {
		return zero, err
	}
	if p.Card.Version != version || p.Card.Status != "draft" {
		return p.response(), errProposalVersion
	}
	switch verb {
	case "edit":
		edit, ok := body.(chatentities.ProposalEdit)
		if !ok {
			return zero, chatentities.ErrInvalid
		}
		p.Card.Title, p.Card.Items = edit.Title, edit.Items
		if err = validateProposalCard(p.Card); err != nil {
			return zero, err
		}
		if err = proposalItemsVisible(ctx, tx, p); err != nil {
			return zero, err
		}
		p.Card.Version++
	case "discard":
		p.Card.Status = "discarded"
		p.Card.Version++
	case "confirm":
		outcome := &chatentities.ProposalOutcome{ReviewedVersion: version, ConfirmedBy: a.UserID, ConfirmedAt: tsformat.Format(time.Now()), Issues: []chatentities.CreatedIssueRef{}}
		for _, item := range p.Card.Items {
			if !item.Selected {
				continue
			}
			kind, e := proposalAssignee(ctx, tx, p.Workspace, item.AssigneeID)
			if e != nil {
				return zero, e
			}
			issue, identifier, e := insertIssueTx(ctx, tx, r.logger, issueSpec{WorkspaceID: p.Workspace, CrewID: item.CrewID, Title: item.Title, Description: &item.Description, Priority: item.Priority, ProjectID: item.ProjectID, AssigneeType: kind, AssigneeID: item.AssigneeID, AuthoredVia: "user_api", CreatedByUserID: a.UserID, AuthorAgentID: p.Agent, AuthorChatID: p.Chat, AuthorRunID: p.Run})
			if e != nil {
				return zero, e
			}
			originID, e := captureHumanIssueOrigin(ctx, tx, req, issue)
			if e != nil {
				return zero, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE missions SET chat_origin_id=NULLIF(?,'') WHERE id=? AND workspace_id=?`, originID, issue, p.Workspace); e != nil {
				return zero, e
			}
			if _, e = persistIssueActivityTx(ctx, tx, issueEvent{MissionID: issue, ActorType: "user", ActorID: a.UserID, Action: actionCreated, Details: "Created from chat proposal " + p.ID}); e != nil {
				return zero, e
			}
			receipt, _, e := chatentities.BeginOperation(ctx, tx, chatentities.OperationSpec{ID: generateCUID(), WorkspaceID: p.Workspace, Key: "proposal-issue:" + proposalHash("receipt", op.ID, item.ID, nil), RequestHash: proposalHash("issue.create", p.Chat, p.ID, item), ActorType: "human", ActorID: a.UserID, RunID: p.Run, Provenance: "verified", Kind: "issue", Verb: "created", SourceProposalID: p.ID, SourceProposalVersion: version})
			if e != nil {
				return zero, e
			}
			var revision, aggregate int64
			if e = tx.QueryRowContext(ctx, `SELECT revision,aggregate_revision FROM missions WHERE id=?`, issue).Scan(&revision, &aggregate); e != nil {
				return zero, e
			}
			raw, _ := json.Marshal(map[string]string{"id": issue, "identifier": identifier})
			if e = chatentities.CompleteOperation(ctx, tx, receipt.ID, chatentities.ResultSpec{EntityID: issue, Revision: revision, AggregateRevision: aggregate, Result: raw}); e != nil {
				return zero, e
			}
			if _, e = chatentities.AppendRef(ctx, tx, chatentities.RefSpec{EventID: generateCUID(), ChatID: p.Chat, OperationID: receipt.ID, RunID: p.Run, TriggerMessageID: p.Trigger}); e != nil {
				return zero, e
			}
			outcome.Issues = append(outcome.Issues, chatentities.CreatedIssueRef{ID: issue, Identifier: identifier})
		}
		if len(outcome.Issues) == 0 {
			return zero, chatentities.ErrInvalid
		}
		p.Card.Status = "accepted"
		p.Card.Outcome = outcome
		p.Card.Version++
	default:
		return zero, chatentities.ErrInvalid
	}
	if p.Card.Version > maxCardInteger {
		return zero, chatentities.ErrInvalid
	}
	items, _ := json.Marshal(p.Card.Items)
	var outcome any
	if p.Card.Outcome != nil {
		raw, _ := json.Marshal(p.Card.Outcome)
		outcome = string(raw)
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat_issue_proposals SET title=?,items=?,version=?,status=?,outcome=? WHERE id=? AND status='draft'`, p.Card.Title, string(items), p.Card.Version, p.Card.Status, outcome, id)
	if err != nil {
		return zero, err
	}
	result := p.response()
	raw, _ := json.Marshal(result)
	if err = completeProposalReceipt(ctx, tx, op, raw); err != nil {
		return zero, err
	}
	return result, tx.Commit()
}

// Reserve identity in memory, then append the completed receipt inside the same
// write transaction. A source write locks SQLite before lookup/insert.
type proposalReceipt struct {
	ID, Workspace, ActorType, Actor, Key, Hash, Verb, Run, ProposalID string
	Result                                                            json.RawMessage
}

func beginProposalReceipt(ctx context.Context, tx *sql.Tx, ws, kind, actor, key, hash, verb, run, id string) (proposalReceipt, bool, error) {
	p := proposalReceipt{ID: generateCUID(), Workspace: ws, ActorType: kind, Actor: actor, Key: key, Hash: hash, Verb: verb, Run: run, ProposalID: id}
	if len(key) > 200 || strings.TrimSpace(key) == "" {
		return p, false, chatentities.ErrKeyRequired
	}
	var stored proposalReceipt
	var result string
	err := tx.QueryRowContext(ctx, `SELECT id,request_hash,verb,run_id,proposal_id,result FROM chat_proposal_operations WHERE workspace_id=? AND actor_type=? AND actor_id=? AND operation_key=?`, ws, kind, actor, key).Scan(&stored.ID, &stored.Hash, &stored.Verb, &stored.Run, &stored.ProposalID, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if stored.Hash != hash || stored.Verb != verb || stored.Run != run {
		return p, false, chatentities.ErrKeyConflict
	}
	stored.Result = json.RawMessage(result)
	return stored, true, nil
}
func completeProposalReceipt(ctx context.Context, tx *sql.Tx, p proposalReceipt, result []byte) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO chat_proposal_operations(id,workspace_id,actor_type,actor_id,operation_key,request_hash,verb,run_id,proposal_id,result) VALUES(?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Workspace, p.ActorType, p.Actor, p.Key, p.Hash, p.Verb, p.Run, p.ProposalID, string(result))
	return err
}

// Owner-agent revisions are inert proposal edits. Active verified origin is
// required here; human confirmation deliberately does not use that admission.
// Later turns may revise the same agent/chat source without changing its origin.
func (r *Router) editAgentProposal(ctx context.Context, run, agent, workspace, id, key string, body chatentities.ProposalEdit) (ChatCardResponse, error) {
	var zero ChatCardResponse
	if body.ExpectedVersion < 1 || body.ExpectedVersion > maxCardInteger {
		return zero, chatentities.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE chat_issue_proposals SET version=version WHERE id=? AND status='draft'`, id); err != nil {
		return zero, err
	}
	if err = proposalAgentTokenScope(ctx, tx, workspace, agent); err != nil {
		return zero, err
	}
	a, err := runauthority.Resolve(ctx, tx, run, agent, workspace)
	if err != nil {
		return zero, err
	}
	if a.HumanAuthority == nil || a.ChatID == "" || a.TriggerMessageID == "" {
		return zero, errProposalForbidden
	}
	p, err := loadProposal(ctx, tx, a.ChatID, id)
	if err != nil {
		return zero, err
	}
	if p.Workspace != workspace || p.Agent != agent {
		return zero, sql.ErrNoRows
	}
	if err = proposalAgentItemsVisible(ctx, tx, p, agent); err != nil {
		return zero, err
	}
	op, replay, err := beginProposalReceipt(ctx, tx, workspace, "agent", agent, key, proposalHash("edit", p.Chat, id, body), "edit", run, id)
	if err != nil {
		return zero, err
	}
	if replay {
		var result ChatCardResponse
		if err = json.Unmarshal(op.Result, &result); err != nil {
			return zero, err
		}
		if p.Card.Status != "draft" || result.Proposal == nil || p.Card.Version != result.Proposal.Version {
			return p.response(), nil
		}
		return result, nil
	}
	if body.ExpectedVersion != p.Card.Version || p.Card.Status != "draft" {
		return p.response(), errProposalVersion
	}
	p.Card.Title, p.Card.Items = body.Title, body.Items
	p.Card.Version++
	if err = validateProposalCard(p.Card); err != nil {
		return zero, err
	}
	if err = proposalAgentItemsVisible(ctx, tx, p, agent); err != nil {
		return zero, err
	}
	for _, item := range p.Card.Items {
		var one int
		if err = tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND crew_id=? AND deleted_at IS NULL`, agent, workspace, item.CrewID).Scan(&one); err != nil {
			return zero, err
		}
	}
	raw, _ := json.Marshal(p.Card.Items)
	if _, err = tx.ExecContext(ctx, `UPDATE chat_issue_proposals SET version=?,title=?,items=? WHERE id=? AND status='draft'`, p.Card.Version, p.Card.Title, string(raw), id); err != nil {
		return zero, err
	}
	result := p.response()
	raw, _ = json.Marshal(result)
	if err = completeProposalReceipt(ctx, tx, op, raw); err != nil {
		return zero, err
	}
	return result, tx.Commit()
}

func proposalAgentItemsVisible(ctx context.Context, q chatentities.Queryer, p storedProposal, agent string) error {
	if err := proposalItemsVisible(ctx, q, p); err != nil {
		return err
	}
	for _, item := range p.Card.Items {
		var one int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND crew_id=? AND deleted_at IS NULL`, agent, p.Workspace, item.CrewID).Scan(&one); err != nil {
			return err
		}
	}
	return nil
}

// Preserve the existing internal IPC workspace/crew binding inside the same
// source-write snapshot, including a concurrent agent crew reassignment.
func proposalAgentTokenScope(ctx context.Context, q chatentities.Queryer, workspace, agent string) error {
	if bound := InternalTokenWorkspaceFromContext(ctx); bound != "" && bound != workspace {
		return errProposalForbidden
	}
	if crew := InternalTokenCrewFromContext(ctx); crew != "" {
		var one int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND crew_id=? AND deleted_at IS NULL`, agent, workspace, crew).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errProposalForbidden
			}
			return err
		}
	}
	return nil
}
