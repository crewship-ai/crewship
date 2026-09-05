package api

// SupplyEscalationCredential — the human half of a credential ASK (#2376).
//
// An agent that needs a secret only a human has raises a CREDENTIAL
// escalation with no value; CreateEscalation stages a REQUESTED credential
// (name, type, tier, purpose — no value) and links it. This handler is where
// the value enters the system, and it is the ONLY place: /resolve refuses
// text on a CREDENTIAL escalation, the CLI reads the value from stdin, and
// the value written here goes into credentials.encrypted_value and nowhere
// else. The agent is answered with a GRANT — the name it may use the
// credential by, through /keeper/execute — and never sees the value.
//
// One transaction: activate the credential, grant it to the asking agent,
// resolve the escalation. Either all three happen or none does; a credential
// that is ACTIVE but ungranted, or an escalation RESOLVED against a row still
// waiting for a value, would each be a state the agent cannot act on and the
// operator cannot see.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

// credentialStatusRequested is the status of a credential row an agent asked
// for and no human has filled yet. Never ACTIVE, so no delivery query serves
// it; disposed of like a PENDING_APPROVAL row when its escalation expires or
// is cancelled.
const credentialStatusRequested = "REQUESTED"

// supplyCredentialRequest is the body of POST /escalations/{id}/supply.
type supplyCredentialRequest struct {
	Value string `json:"value"`
	// Name, Type and SecurityLevel are read only for a LEGACY escalation — one
	// raised without structured metadata, so no REQUESTED row exists to fill.
	// The human names the credential; the agent's escalation is still what it
	// is granted against. On an ask that staged a row, Name and Type are
	// ignored and SecurityLevel, when set, overrides the agent's proposal.
	Name          string `json:"name,omitempty"`
	Type          string `json:"type,omitempty"`
	SecurityLevel int    `json:"security_level,omitempty"`
}

// SupplyEscalationCredential handles POST /api/v1/escalations/{escalationId}/supply.
func (h *QueryHandler) SupplyEscalationCredential(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	// Same gate as ResolveEscalation: MANAGER+ decides CREDENTIAL escalations
	// below L4 (docs/guides/credentials.mdx), and supplying the value IS the
	// decision. The four-eyes rule below tightens it exactly as on resolve.
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var body supplyCredentialRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	// Not trimmed: a secret is bytes, and a value that legitimately starts or
	// ends with whitespace is the caller's to send. Only emptiness is refused.
	if body.Value == "" {
		replyError(w, http.StatusBadRequest, "value required")
		return
	}
	if len(body.Value) > maxCredentialValueLen {
		replyError(w, http.StatusBadRequest, credentialValueTooLongMsg)
		return
	}
	if body.SecurityLevel != 0 && !keeper.SecurityLevel(body.SecurityLevel).Valid() {
		replyError(w, http.StatusBadRequest, "security_level must be 1..4")
		return
	}

	var status, chatID, crewID, fromSlug, fromAgentID, fromAgentName, escalationType string
	var credentialID, initiatorUserID, agentGaveUpAt sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT e.status, e.chat_id, e.crew_id, a.slug, a.id, COALESCE(a.name, ''), e.type,
		       e.credential_id, a.created_by_user_id, e.agent_gave_up_at
		FROM escalations e
		JOIN agents a ON a.id = e.from_agent_id
		WHERE e.id = ? AND e.workspace_id = ?
	`, escalationID, workspaceID).Scan(&status, &chatID, &crewID, &fromSlug, &fromAgentID, &fromAgentName,
		&escalationType, &credentialID, &initiatorUserID, &agentGaveUpAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "escalation not found")
			return
		}
		replyInternalError(w, h.logger, "supply credential: escalation lookup", err)
		return
	}
	if status != escalationStatusPending {
		replyError(w, http.StatusConflict, escalationTerminalError(status))
		return
	}
	if escalationType != "CREDENTIAL" {
		replyError(w, http.StatusBadRequest, "only a CREDENTIAL escalation takes a credential value")
		return
	}
	if h.refuseCredentialSelfApproval(w, r, secondApproverInput{
		workspaceID: workspaceID, crewID: crewID, chatID: chatID, escalationID: escalationID,
		escalationType: escalationType, fromSlug: fromSlug, action: "supply",
		credentialID: credentialID, initiatorUserID: initiatorUserID,
	}) {
		return
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "supply credential: begin tx", err)
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			h.logger.Warn("supply credential: rollback", "error", rbErr)
		}
	}()

	var (
		credID, credName, credType string
		level                      int
	)
	if credentialID.Valid && credentialID.String != "" {
		// The ask staged a row. Fill exactly that row, and only while it is
		// still waiting for a value.
		var credStatus string
		if err := tx.QueryRowContext(r.Context(), `
			SELECT name, type, COALESCE(security_level, 1), status FROM credentials
			WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			credentialID.String, workspaceID).Scan(&credName, &credType, &level, &credStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusConflict, "the credential this escalation staged no longer exists")
				return
			}
			replyInternalError(w, h.logger, "supply credential: credential lookup", err)
			return
		}
		switch credStatus {
		case credentialStatusRequested:
		case "PENDING_APPROVAL":
			replyError(w, http.StatusConflict,
				"the agent proposed a value for this credential — approve or reject it via /resolve, there is nothing to supply")
			return
		default:
			replyError(w, http.StatusConflict, fmt.Sprintf("credential is %s, not waiting for a value", credStatus))
			return
		}
		credID = credentialID.String
		if body.SecurityLevel != 0 {
			level = body.SecurityLevel
		}
		enc, ok := encryptOrError(w, h.logger, "supply credential: encrypt", body.Value)
		if !ok {
			return
		}
		// The CAS on status is what makes two humans answering the same ask
		// at once safe: the second UPDATE matches no row and is told so.
		res, err := tx.ExecContext(r.Context(), `
			UPDATE credentials
			   SET encrypted_value = ?, status = 'ACTIVE', handle_only = 1, security_level = ?,
			       approved_by_user_id = ?, approved_at = ?, created_by = ?, last_error = NULL, updated_at = ?
			 WHERE id = ? AND workspace_id = ? AND status = ? AND deleted_at IS NULL`,
			enc, level, user.ID, nowStr, user.ID, nowStr, credID, workspaceID, credentialStatusRequested)
		if err != nil {
			replyInternalError(w, h.logger, "supply credential: activate", err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			replyError(w, http.StatusConflict, "credential is no longer waiting for a value")
			return
		}
	} else {
		// A LEGACY ask — free text, no staged row. The human names what they
		// are storing; the row is created ACTIVE and handle-only in one step,
		// attributed to the asking agent as requester and this user as the one
		// who supplied and thereby approved it.
		name, ok := credname.Canonical(strings.TrimSpace(body.Name))
		if strings.TrimSpace(body.Name) == "" || !ok {
			replyError(w, http.StatusBadRequest,
				"name required: this escalation staged no credential, so name the one you are supplying (an environment variable name, e.g. PG_PASSWORD)")
			return
		}
		credType = strings.ToUpper(strings.TrimSpace(body.Type))
		if credType == "" {
			credType = string(CredTypeSecret)
		}
		if msg := validateCredentialType(credType); msg != "" {
			replyError(w, http.StatusBadRequest, msg)
			return
		}
		level = 1
		if body.SecurityLevel != 0 {
			level = body.SecurityLevel
		}
		credName = name
		// Mirror createPendingCredential: a soft-deleted namesake is cleared so
		// the UNIQUE(workspace_id, name) constraint cannot trip; a live one is a
		// conflict the human resolves by hand (never auto-rename a secret).
		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
			workspaceID, credName); err != nil {
			replyInternalError(w, h.logger, "supply credential: clear soft-deleted namesake", err)
			return
		}
		var existing int
		if err := tx.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL",
			workspaceID, credName).Scan(&existing); err != nil {
			replyInternalError(w, h.logger, "supply credential: name probe", err)
			return
		}
		if existing > 0 {
			replyError(w, http.StatusConflict,
				fmt.Sprintf("a credential named %q already exists — grant it to the agent instead, or choose another name", credName))
			return
		}
		enc, ok := encryptOrError(w, h.logger, "supply credential: encrypt", body.Value)
		if !ok {
			return
		}
		credID = generateCUID()
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
				type, provider, scope, security_level, status, created_by, created_at, updated_at,
				created_by_actor_type, created_by_actor_id, handle_only, approved_by_user_id, approved_at)
			VALUES (?, ?, ?, '', ?, ?, 'NONE', 'WORKSPACE', ?, 'ACTIVE', ?, ?, ?, 'agent', ?, 1, ?, ?)`,
			credID, workspaceID, credName, enc, credType, level, user.ID, nowStr, nowStr,
			fromAgentID, user.ID, nowStr); err != nil {
			replyInternalError(w, h.logger, "supply credential: insert", err)
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE escalations SET credential_id = ? WHERE id = ? AND workspace_id = ?`,
			credID, escalationID, workspaceID); err != nil {
			replyInternalError(w, h.logger, "supply credential: link", err)
			return
		}
	}

	// The grant IS the answer. A standing row here; the lease, when the
	// workspace opted in, is issued on top of it after commit by the one writer
	// every lease goes through (issueCredentialLease) — see
	// grantLeasedCredentialOnApprove for why the INSERT must not carry lease
	// columns of its own.
	envVar, _ := credname.Canonical(credName)
	if _, err := tx.ExecContext(r.Context(), `
		INSERT OR IGNORE INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`,
		generateCUID(), fromAgentID, credID, envVar, nowStr); err != nil {
		replyInternalError(w, h.logger, "supply credential: grant", err)
		return
	}

	// resolution stays NULL: the value is in the vault, and there is nothing
	// else this row has to say (#2376).
	res, err := tx.ExecContext(r.Context(), `
		UPDATE escalations SET status = 'RESOLVED', resolution = NULL, action = 'approve',
		       resolved_at = ?, resolved_by = 'user'
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING'`,
		nowStr, escalationID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "supply credential: resolve escalation", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusConflict, "escalation is no longer pending")
		return
	}
	if err := RecordCredentialEventTx(r.Context(), tx, credID, AuditEventApproved, "", clientIP(r),
		map[string]any{"approved_by": user.ID, "supplied": true, "handle_only": true, "escalation_id": escalationID}); err != nil {
		replyInternalError(w, h.logger, "supply credential: audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "supply credential: commit", err)
		return
	}

	// Everything below is best-effort bookkeeping on a decision that is
	// already durable, on a context that outlives the request so a client
	// hanging up cannot leave the agent waiting on a grant that exists.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()

	if ttl := governance.Resolve(ctx, h.db, h.logger, workspaceID).AutoLeaseSeconds; ttl > 0 {
		issueCredentialLease(ctx, h.db, h.logger, h.journal, leaseIssueInput{
			WorkspaceID: workspaceID, CrewID: crewID, AgentID: fromAgentID, AgentName: fromAgentName,
			CredentialID: credID, CredentialName: credName, SecurityLevel: level,
			Source: leaseSourceEscalationSupply, RequestID: escalationID, TTLSeconds: ttl,
		})
	}

	inbox.ResolveBySource(ctx, h.db, h.logger, "escalation", escalationID, "approve", user.ID)

	agentStillWaiting := !agentGaveUpAt.Valid || agentGaveUpAt.String == ""
	_, _ = h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     fromAgentID,
		Type:        journal.EntryPeerEscalation,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     user.ID,
		Summary:     fmt.Sprintf("escalation %s resolved (credential %s supplied)", escalationID, credName),
		Payload: map[string]any{
			"action":              "approve",
			"state":               "resolved",
			"escalation_type":     escalationType,
			"credential_id":       credID,
			"credential_name":     credName,
			"handle_only":         true,
			"supplied":            true,
			"agent_still_waiting": agentStillWaiting,
			"agent_gave_up_at":    agentGaveUpAt.String,
		},
		Refs: map[string]any{"escalation_id": escalationID, "credential_id": credID},
	})

	handle := h.credentialHandleFor(ctx, workspaceID, credID, fromAgentID)
	h.notifyEscalationWaiter(escalationID, escalationResult{
		Action:               "approve",
		Credential:           handle,
		CredentialEscalation: true,
	})
	broadcastChannelEvent(h.hub, "session", chatID, "escalation_resolved",
		map[string]string{"id": escalationID, "resolution": "[credential supplied]", "action": "approve"})
	broadcastWorkspaceEvent(h.hub, workspaceID, "escalation.resolved",
		map[string]string{"id": escalationID, "crew_id": crewID, "from_slug": fromSlug, "action": "approve"})

	h.logger.Info("credential supplied for escalation",
		"escalation_id", escalationID, "credential_id", credID, "credential", credName,
		"agent_id", fromAgentID, "crew_id", crewID)

	resp := map[string]any{
		"id":                  escalationID,
		"status":              escalationStatusResolved,
		"action":              "approve",
		"credential":          handle,
		"agent_still_waiting": agentStillWaiting,
	}
	if !agentStillWaiting {
		resp["agent_gave_up_at"] = agentGaveUpAt.String
		resp["note"] = fmt.Sprintf(
			"Stored and granted, but %s stopped waiting at %s and continued without it — that run will not receive the answer. "+
				"The credential is active in the vault and granted to the agent, so its next run can use it.",
			fromSlug, agentGaveUpAt.String)
	}
	writeJSON(w, http.StatusOK, resp)
}
