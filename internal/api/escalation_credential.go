package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// metadataCarriesValue reports whether the escalate metadata is JSON with a
// non-empty "value" field — i.e. it embeds a secret. Used to redact defensively
// even when the proposal is malformed (missing name, bad type, ...), so a
// secret can never reach escalations.metadata, ListEscalations, or the journal.
func metadataCarriesValue(metadata string) bool {
	s := strings.TrimSpace(metadata)
	if s == "" || s[0] != '{' {
		return false
	}
	var m struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		// Malformed JSON object — we cannot trust it doesn't embed a secret
		// (e.g. an unterminated `{"value":"secret"`). Fail closed: if it so
		// much as mentions a "value" key, treat it as secret-bearing so the
		// caller redacts instead of persisting/journaling the raw string. A
		// false positive here only over-redacts a non-secret; the inverse
		// would leak.
		return strings.Contains(s, `"value"`)
	}
	return strings.TrimSpace(m.Value) != ""
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

// pendingCredResult classifies the outcome of staging an agent-proposed
// credential so the caller can react honestly. The distinction matters: a
// recoverable mismatch (name collision, unknown type) should still surface a
// plain escalation to a human, but a hard failure (no approver, vault error)
// must NOT be recorded as a PENDING escalation that falsely claims a proposal
// is waiting for one-click approval when the secret has already been discarded.
type pendingCredResult int

const (
	// pendingCredStaged: the credential row was created PENDING_APPROVAL; the
	// caller links it, redacts the metadata, and records the escalation.
	pendingCredStaged pendingCredResult = iota
	// pendingCredNameConflict: a live credential already uses the proposed name
	// (we never auto-rename). Recoverable — record a plain escalation with a
	// human-readable note, no credential link.
	pendingCredNameConflict
	// pendingCredInvalidType: the proposal's Type is unknown. Recoverable —
	// record a plain escalation with a note, no credential link.
	pendingCredInvalidType
	// pendingCredValueTooLarge: the proposed value exceeds
	// maxCredentialValueLen — the same cap create/update/rotate enforce;
	// without it the escalation flow would be the one uncapped path into
	// the vault. Recoverable — plain escalation with a note.
	pendingCredValueTooLarge
	// pendingCredNoApprover: no workspace OWNER exists to attribute/approve the
	// credential. Hard failure — the caller must NOT record an escalation.
	pendingCredNoApprover
	// pendingCredVaultError: encrypt or insert failed. Hard failure — the caller
	// must NOT record an escalation (the agent should retry).
	pendingCredVaultError
)

// prependEscalationNote puts a human-readable note at the top of an
// escalation's context, keeping any agent-supplied body below a blank line. Used
// when a credential proposal could not be staged but a plain escalation is still
// warranted, so the reporter isn't left thinking a one-click approval is waiting.
func prependEscalationNote(existing, note string) string {
	if strings.TrimSpace(existing) == "" {
		return note
	}
	return note + "\n\n" + existing
}

// createPendingCredential inserts an agent-proposed credential in
// PENDING_APPROVAL state. Such a row is filtered out of every credential
// delivery path (agent_config, keeper, models, auto-assign), so no agent can
// use it until a human flips it to ACTIVE via ResolveEscalation.
//
// Returns (credentialID, pendingCredStaged) on success. On failure the second
// return classifies why (see pendingCredResult); the credentialID is "". The
// caller decides — from the class — whether a plain escalation is still
// warranted (recoverable) or the whole request must fail loud (hard failure).
func (h *QueryHandler) createPendingCredential(ctx context.Context, wsID, fromAgentID string, p credentialProposal) (string, pendingCredResult) {
	if msg := validateCredentialType(p.Type); msg != "" {
		h.logger.Warn("pending credential: invalid type, falling back to plain escalation",
			"type", p.Type, "agent_id", fromAgentID)
		return "", pendingCredInvalidType
	}
	if len(p.Value) > maxCredentialValueLen {
		h.logger.Warn("pending credential: value exceeds cap, falling back to plain escalation",
			"bytes", len(p.Value), "agent_id", fromAgentID)
		return "", pendingCredValueTooLarge
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
		// No OWNER row is a permanent config problem (fail loud, don't stage);
		// any other DB error is transient — surface it as a vault error so the
		// agent retries instead of being told "no approver" forever.
		if errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("pending credential: no workspace owner to attribute",
				"workspace_id", wsID)
			return "", pendingCredNoApprover
		}
		h.logger.Error("pending credential: owner lookup failed", "workspace_id", wsID, "error", err)
		return "", pendingCredVaultError
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
		return "", pendingCredNameConflict
	}

	enc, err := encryption.Encrypt(p.Value)
	if err != nil {
		h.logger.Error("pending credential: encrypt", "error", err)
		return "", pendingCredVaultError
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
		return "", pendingCredVaultError
	}

	recordCredentialEventBestEffort(ctx, h.db, h.logger, credID,
		AuditEventCreated, fromAgentID, "", map[string]any{
			"status":     "PENDING_APPROVAL",
			"actor_type": "agent",
			"proposed":   true,
		})

	h.logger.Info("agent proposed credential (pending approval)",
		"credential_id", credID, "name", p.Name, "agent_id", fromAgentID)
	return credID, pendingCredStaged
}
