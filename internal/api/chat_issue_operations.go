package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func chatIssueOriginID(op *chatIssueOperation) string {
	if op == nil {
		return ""
	}
	return op.authority.ChatOriginID
}

type issueOperationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type chatIssueOperation struct {
	authority   runauthority.Authority
	operation   *chatentities.Operation
	requestHash string
	actorType   string
	actorID     string
	crewID      string
}

// issueRequesterAllowed uses the same role/capability decision as the public
// issue routes, with a fresh snapshot rather than the capability TTL cache.
// Public update/comment routes require create; only creation has the explicit
// issue.create capability alternative. It must not become a write-all grant.
func issueRequesterAllowed(ctx context.Context, q issueOperationQueryer, userID, workspaceID, verb string) (bool, error) {
	var role, mode string
	var caps sql.NullString
	err := q.QueryRowContext(ctx, `SELECT role, capabilities, access_mode FROM workspace_members
		WHERE workspace_id=? AND user_id=?`, workspaceID, userID).Scan(&role, &caps, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if mode == "restricted" {
		return false, nil
	}
	if verb == "shown" {
		return canRole(role, "read"), nil
	}
	if canRole(role, "create") {
		return true, nil
	}
	return verb == "created" && HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityIssueCreate), nil
}

func issueViewerActions(ctx context.Context, q issueOperationQueryer, userID, workspaceID, crewID string) ([]string, error) {
	allowed, err := issueRequesterAllowed(ctx, q, userID, workspaceID, "shown")
	if err != nil || !allowed {
		return []string{}, err
	}
	actions := []string{"view"}
	write, err := issueRequesterAllowed(ctx, q, userID, workspaceID, "updated")
	if err != nil {
		return nil, err
	}
	if write {
		actions = append(actions, "comment", "edit_fields", "assign", "change_status", "change_workflow", "attach_files", "link", "start_work", "delete")
	}
	return actions, nil
}

func chatIssueRequestHash(verb, entityID string, payload any) (string, error) {
	b, err := json.Marshal(struct {
		Verb, EntityID string
		Payload        any
	}{verb, entityID, payload})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// authorizeChatIssue never derives a chat or human from tool arguments. A
// legacy call with no run keeps its old API contract; a supplied run must have
// durable host authority, including on a replay and after sidecar restart.
func (h *InternalIssueHandler) authorizeChatIssue(ctx context.Context, q issueOperationQueryer, workspaceID, agentID, runID, key, verb, entityID string, payload any) (*chatIssueOperation, error) {
	if runID == "" {
		if key != "" || runauthority.OriginIDFromContext(ctx) != "" || hostPipelineRunID(ctx) != "" {
			return nil, runauthority.ErrUnavailable
		}
		return nil, nil
	}
	var a runauthority.Authority
	actorType, actorID, crewID := "agent", agentID, ""
	var err error
	if pipelineRun := hostPipelineRunID(ctx); pipelineRun != "" {
		if pipelineRun != runID {
			return nil, runauthority.ErrIdentityMismatch
		}
		originID := runauthority.OriginIDFromContext(ctx)
		var storedOrigin, pipelineID string
		err = q.QueryRowContext(ctx, `SELECT COALESCE(pr.chat_origin_id,''),p.id,p.author_crew_id
			FROM pipeline_runs pr JOIN pipelines p ON p.id=pr.pipeline_id AND p.workspace_id=pr.workspace_id
			JOIN crews c ON c.id=p.author_crew_id AND c.workspace_id=p.workspace_id AND c.deleted_at IS NULL
			LEFT JOIN agents a ON a.id=p.author_agent_id AND a.workspace_id=p.workspace_id AND a.crew_id=p.author_crew_id AND a.deleted_at IS NULL
			WHERE pr.id=? AND pr.workspace_id=? AND pr.status='running'
			AND (a.id=? OR (COALESCE(p.author_agent_id,'')='' AND ?='' AND pr.chat_origin_id IS NULL))
			AND (pr.chat_origin_id IS NOT NULL OR (p.chat_origin_id IS NULL AND NOT EXISTS(
			SELECT 1 FROM pipeline_versions v WHERE v.pipeline_id=p.id AND v.version=pr.pipeline_version AND v.chat_origin_id IS NOT NULL)))`,
			runID, workspaceID, agentID, agentID).Scan(&storedOrigin, &pipelineID, &crewID)
		if err != nil {
			return nil, runauthority.ErrUnavailable
		}
		if storedOrigin != originID {
			return nil, runauthority.ErrIdentityMismatch
		}
		if originID != "" {
			a, err = runauthority.ValidateOrigin(ctx, q, originID, agentID, workspaceID)
			if err != nil {
				return nil, err
			}
		} else {
			a = runauthority.Authority{WorkspaceID: workspaceID, AgentID: agentID}
		}
		if agentID == "" {
			// Only a master-authenticated, persisted, origin-free pipeline can
			// act without an agent. Its actor identity is never taken from JSON.
			actorType, actorID = "system", "pipeline:"+pipelineID+":crew:"+crewID
		}
		// Creation has no existing entity to scope. Fence its requested crew
		// against the persisted recipe just as updates fence their target.
		if verb == "created" {
			body, marshalErr := json.Marshal(payload)
			var target struct {
				CrewID string `json:"crew_id"`
			}
			if marshalErr != nil || json.Unmarshal(body, &target) != nil || target.CrewID != crewID {
				return nil, runauthority.ErrIdentityMismatch
			}
		}
		a.RunID = runID
	} else if originID := runauthority.OriginIDFromContext(ctx); originID != "" {
		a, err = runauthority.ValidateOrigin(ctx, q, originID, agentID, workspaceID)
		if err != nil {
			return nil, err
		}
		var valid bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE id=? AND workspace_id=? AND chat_origin_id=? AND status='running')`, runID, workspaceID, originID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, runauthority.ErrUnavailable
		}
		a.RunID = runID
	} else {
		a, err = runauthority.Resolve(ctx, q, runID, agentID, workspaceID)
		if err != nil {
			return nil, err
		}
	}
	op := &chatIssueOperation{authority: a, actorType: actorType, actorID: actorID, crewID: crewID}
	if entityID != "" {
		if err := verifyChatIssueOperationEntity(ctx, q, op, workspaceID, entityID); err != nil {
			return nil, err
		}
	}
	if a.HumanAuthority == nil && key == "" {
		return nil, nil
	} // keyless host-admitted autonomous compatibility
	if key == "" {
		return nil, chatentities.ErrKeyRequired
	}
	if len(key) > 200 {
		return nil, chatentities.ErrInvalid
	}
	if a.HumanAuthority != nil {
		allowed, err := issueRequesterAllowed(ctx, q, a.HumanAuthority.UserID, workspaceID, verb)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, errChatIssueRequesterForbidden
		}
	}
	hash, err := chatIssueRequestHash(verb, entityID, payload)
	if err != nil {
		return nil, err
	}
	op.requestHash = hash
	return op, nil
}

var errChatIssueRequesterForbidden = errors.New("requester_forbidden")
var errChatIssueEntityUnavailable = errors.New("operation_entity_unavailable")

func verifyChatIssueReplayEntity(ctx context.Context, q issueOperationQueryer, agentID, workspaceID, entityID string) error {
	var readable bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM missions m JOIN agents a ON a.id=? AND a.workspace_id=m.workspace_id AND a.crew_id=m.crew_id AND a.deleted_at IS NULL WHERE m.id=? AND m.workspace_id=? AND m.mission_type='issue')`, agentID, entityID, workspaceID).Scan(&readable); err != nil {
		return err
	}
	if !readable {
		return errChatIssueEntityUnavailable
	}
	return nil
}

// Agentless pipeline replays use the persisted recipe crew, never a supplied
// actor identifier. All other paths retain the live-agent entity check.
func verifyChatIssueOperationEntity(ctx context.Context, q issueOperationQueryer, op *chatIssueOperation, workspaceID, entityID string) error {
	if op.actorType != "system" {
		return verifyChatIssueReplayEntity(ctx, q, op.authority.AgentID, workspaceID, entityID)
	}
	var readable bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM missions WHERE id=? AND workspace_id=? AND crew_id=? AND mission_type='issue')`, entityID, workspaceID, op.crewID).Scan(&readable); err != nil {
		return err
	}
	if !readable {
		return errChatIssueEntityUnavailable
	}
	return nil
}

// checkChatIssueReplay runs before state-transition validation. A valid retry
// must not be rejected because its first successful mutation changed the state.
func (h *InternalIssueHandler) checkChatIssueReplay(w http.ResponseWriter, r *http.Request, workspaceID, agentID, runID, key, verb, entityID string, payload any) bool {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChatIssueError(w, err)
		return true
	}
	defer tx.Rollback()
	op, err := h.authorizeChatIssue(r.Context(), tx, workspaceID, agentID, runID, key, verb, entityID, payload)
	if err != nil {
		writeChatIssueError(w, err)
		return true
	}
	if op == nil {
		return false
	}
	stored, err := chatentities.FindOperation(r.Context(), tx, workspaceID, op.actorType, op.actorID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		writeChatIssueError(w, err)
		return true
	}
	if stored.RequestHash != op.requestHash || stored.RunID != runID {
		writeChatIssueError(w, chatentities.ErrKeyConflict)
		return true
	}
	if err := verifyChatIssueOperationEntity(r.Context(), tx, op, workspaceID, stored.EntityID); err != nil {
		writeChatIssueError(w, err)
		return true
	}
	writeChatIssueReplay(w, stored)
	return true
}

func (h *InternalIssueHandler) beginChatIssueOperation(ctx context.Context, tx *sql.Tx, workspaceID, agentID, runID, key, verb, entityID string, payload any) (*chatIssueOperation, bool, error) {
	op, err := h.authorizeChatIssue(ctx, tx, workspaceID, agentID, runID, key, verb, entityID, payload)
	if err != nil || op == nil {
		return op, false, err
	}
	if op.authority.HumanAuthority != nil && op.authority.ChatOriginID == "" {
		origin, err := runauthority.CaptureOrigin(ctx, tx, runID, agentID, workspaceID)
		if err != nil {
			return nil, false, err
		}
		op.authority.ChatOriginID = origin.ID
	}
	authorityJSON, err := json.Marshal(op.authority.HumanAuthority)
	if err != nil {
		return nil, false, err
	}
	stored, replay, err := chatentities.BeginOperation(ctx, tx, chatentities.OperationSpec{
		ID: generateCUID(), WorkspaceID: workspaceID, Key: key, RequestHash: op.requestHash,
		ActorType: op.actorType, ActorID: op.actorID, RunID: runID, HumanAuthority: authorityJSON,
		Provenance: "verified", Kind: "issue", Verb: verb,
	})
	if err != nil {
		return nil, false, err
	}
	if stored.RunID != runID {
		return nil, false, chatentities.ErrKeyConflict
	}
	if replay {
		if err := verifyChatIssueOperationEntity(ctx, tx, op, workspaceID, stored.EntityID); err != nil {
			return nil, false, err
		}
	}
	op.operation = stored
	return op, replay, nil
}

func completeChatIssueOperation(ctx context.Context, tx *sql.Tx, op *chatIssueOperation, entityID string, result any) error {
	if op == nil {
		return nil
	}
	var revision, aggregate int64
	if err := tx.QueryRowContext(ctx, `SELECT revision, aggregate_revision FROM missions WHERE id=? AND workspace_id=?`, entityID, op.authority.WorkspaceID).Scan(&revision, &aggregate); err != nil {
		return err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if err := chatentities.CompleteOperation(ctx, tx, op.operation.ID, chatentities.ResultSpec{
		EntityID: entityID, Revision: revision, AggregateRevision: aggregate, Result: body,
	}); err != nil {
		return err
	}
	if op.authority.HumanAuthority == nil {
		return nil
	}
	_, err = chatentities.AppendRef(ctx, tx, chatentities.RefSpec{
		EventID: generateCUID(), ChatID: op.authority.ChatID, OperationID: op.operation.ID,
		RunID: op.authority.RunID, TriggerMessageID: op.authority.TriggerMessageID,
	})
	return err
}

func writeChatIssueReplay(w http.ResponseWriter, op *chatentities.Operation) {
	var result map[string]any
	if err := json.Unmarshal(op.Result, &result); err != nil {
		replyError(w, 500, "operation_result_unavailable")
		return
	}
	result["operation_id"] = op.ID
	result["replayed"] = true
	writeJSON(w, http.StatusOK, result)
}

func writeChatIssueError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "chat_operation_unavailable"
	switch {
	case errors.Is(err, errChatIssueEntityUnavailable):
		status, code = 404, "operation_entity_unavailable"
	case errors.Is(err, chatentities.ErrKeyRequired):
		status, code = 400, "operation_key_required"
	case errors.Is(err, chatentities.ErrKeyConflict):
		status, code = 409, "operation_key_conflict"
	case errors.Is(err, chatentities.ErrInvalid):
		status, code = 400, "operation_invalid"
	case errors.Is(err, runauthority.ErrUnavailable):
		status, code = 403, "run_authority_unavailable"
	case errors.Is(err, runauthority.ErrIdentityMismatch):
		status, code = 403, "run_identity_mismatch"
	case errors.Is(err, runauthority.ErrRequesterStale):
		status, code = 403, "requester_authority_stale"
	case errors.Is(err, errChatIssueRequesterForbidden):
		status, code = 403, "requester_forbidden"
	}
	replyError(w, status, code)
}
