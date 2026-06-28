package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// credentialProposal is the structured payload an agent sends in a CREDENTIAL
// escalation's `metadata` field to propose a secret for the vault. The agent
// generated the value (e.g. a DB password for infra it set up); the server
// stores it as a PENDING_APPROVAL credential and a human approves it.
type credentialProposal struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Value    string `json:"value"`
}

// redactedMetadata returns the proposal as JSON with the secret value stripped,
// safe to persist on the escalation row, surface in ListEscalations, and emit to
// the journal. The raw proposal (with `value`) must NEVER be written anywhere
// except the encrypted credentials.encrypted_value column.
func (p credentialProposal) redactedMetadata(credentialID string) string {
	b, _ := json.Marshal(map[string]string{
		"name":          p.Name,
		"type":          p.Type,
		"provider":      p.Provider,
		"credential_id": credentialID,
	})
	return string(b)
}

// parseCredentialProposal decodes the escalate `metadata` JSON. ok=false (no
// error) when the metadata is absent or not a usable credential proposal — the
// caller then falls back to a plain CREDENTIAL escalation (the legacy
// human-supplies-the-secret flow). Defaults type→SECRET, provider→NONE.
func parseCredentialProposal(metadata string) (credentialProposal, bool) {
	s := strings.TrimSpace(metadata)
	if s == "" || s[0] != '{' {
		return credentialProposal{}, false
	}
	var p credentialProposal
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return credentialProposal{}, false
	}
	p.Name = strings.TrimSpace(p.Name)
	p.Type = strings.TrimSpace(p.Type)
	p.Provider = strings.TrimSpace(p.Provider)
	if p.Name == "" || p.Value == "" {
		return credentialProposal{}, false
	}
	if p.Type == "" {
		p.Type = "SECRET"
	}
	if p.Provider == "" {
		p.Provider = "NONE"
	}
	return p, true
}

// createPendingCredential inserts an agent-proposed credential in
// PENDING_APPROVAL state. Such a row is filtered out of every credential
// delivery path (agent_config, keeper, models, auto-assign), so no agent can
// use it until a human flips it to ACTIVE via ResolveEscalation.
//
// Returns (credentialID, true) on success. (─, false) means "could not create a
// pending row — fall back to a plain escalation": invalid type, a live name
// collision (we never auto-rename, which would break the agent's env-var
// mapping), no workspace owner to attribute the row to, or an encrypt/insert
// failure. The escalation itself still gets created either way.
func (h *QueryHandler) createPendingCredential(ctx context.Context, wsID, fromAgentID string, p credentialProposal) (string, bool) {
	if msg := validateCredentialType(p.Type); msg != "" {
		h.logger.Warn("pending credential: invalid type, falling back to plain escalation",
			"type", p.Type, "agent_id", fromAgentID)
		return "", false
	}

	// credentials.created_by is NOT NULL → users(id); an agent has no human
	// identity, so attribute the proposal to the workspace OWNER. The approver
	// overwrites created_by on activation; created_by_actor_* preserves the
	// agent as the original proposer for the audit trail.
	var ownerID string
	if err := h.db.QueryRowContext(ctx, `
		SELECT user_id FROM workspace_members
		WHERE workspace_id = ? AND role = 'OWNER'
		ORDER BY created_at ASC LIMIT 1
	`, wsID).Scan(&ownerID); err != nil {
		h.logger.Warn("pending credential: no workspace owner to attribute, falling back",
			"workspace_id", wsID, "error", err)
		return "", false
	}

	// Clear any soft-deleted same-name row so the INSERT can't trip the
	// UNIQUE(workspace_id, name) constraint (mirrors credentials_mutate.go).
	if _, err := h.db.ExecContext(ctx,
		"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
		wsID, p.Name); err != nil {
		h.logger.Warn("pending credential: cleanup soft-deleted", "name", p.Name, "error", err)
	}
	// A live credential with this name already exists → do not auto-rename.
	var existing int
	if err := h.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL",
		wsID, p.Name).Scan(&existing); err == nil && existing > 0 {
		h.logger.Warn("pending credential: name already in use, falling back to plain escalation",
			"name", p.Name)
		return "", false
	}

	enc, err := encryption.Encrypt(p.Value)
	if err != nil {
		h.logger.Error("pending credential: encrypt", "error", err)
		return "", false
	}

	credID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, security_level, status, created_by, created_at, updated_at,
			created_by_actor_type, created_by_actor_id)
		VALUES (?, ?, ?, '', ?, ?, ?, 'WORKSPACE', 1, 'PENDING_APPROVAL', ?, ?, ?, 'agent', ?)`,
		credID, wsID, p.Name, enc, p.Type, p.Provider, ownerID, now, now, fromAgentID); err != nil {
		h.logger.Error("pending credential: insert", "error", err, "name", p.Name)
		return "", false
	}

	if auditErr := RecordCredentialEvent(ctx, h.db, h.logger, credID,
		AuditEventCreated, fromAgentID, "", map[string]any{
			"status":     "PENDING_APPROVAL",
			"actor_type": "agent",
			"proposed":   true,
		}); auditErr != nil {
		h.logger.Warn("pending credential: audit", "error", auditErr)
	}

	h.logger.Info("agent proposed credential (pending approval)",
		"credential_id", credID, "name", p.Name, "agent_id", fromAgentID)
	return credID, true
}
