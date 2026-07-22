package api

// Webhook signing-secret rotation (#999).
//
// The secret follows show-once semantics, mirroring CLI tokens and
// notification-channel secrets: no endpoint returns a stored secret back
// (the internal plaintext read was removed in the same change), so
// rotation is the ONLY way to obtain one. Mint new → return exactly once
// → the previous secret stops validating immediately.

import (
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// encryptionNotConfiguredMsg is the actionable error surfaced when a secret
// write fails closed (#1254 item 1): it names both the fix and the explicit
// opt-out, mirroring the server-log message from encryption.EncryptAtRest.
const encryptionNotConfiguredMsg = "Cannot store the secret: no encryption key is configured on the server. " +
	"Set ENCRYPTION_KEY (openssl rand -hex 32), or set " +
	encryption.AllowPlaintextSecretsEnvVar + "=true to explicitly accept plaintext storage"

// RotateWebhookSecret mints a fresh webhook signing secret for an agent
// and returns it exactly once.
// POST /api/v1/agents/{agentId}/webhook-secret/rotate
func (h *AgentHandler) RotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	callerUserID := ""
	if user != nil {
		callerUserID = user.ID
	}

	// Same per-agent edit gate as Update: OWNER/ADMIN always; MANAGER for
	// agents they created or crews they admin; MEMBER/VIEWER refused.
	ok, err := canEditAgent(r.Context(), h.db, callerUserID, role, agentID)
	if err != nil {
		h.logger.Error("webhook secret rotate gate", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !ok {
		replyForbidden(w, h.logger, callerUserID, role,
			"agent.webhook_secret.rotate", "agent:"+agentID)
		return
	}

	secret := generateWebhookSecret()
	// #1072/#1029: store the secret AES-256-GCM encrypted at rest (like
	// credentials), not plaintext. #1254 item 1: fail-CLOSED — with no usable
	// key EncryptAtRest refuses (rotate is rejected, the stored secret stays
	// untouched) unless the operator explicitly set
	// CREWSHIP_ALLOW_PLAINTEXT_SECRETS=true. The show-once response below
	// still returns the PLAINTEXT `secret`.
	storedSecret, encrypted, encErr := encryption.EncryptAtRest(secret)
	if encErr != nil {
		h.logger.Error("webhook secret encrypt", "agent_id", agentID, "error", encErr)
		if errors.Is(encErr, encryption.ErrPlaintextRefused) {
			// Misconfiguration, not an internal fault: the caller passed the
			// admin-level edit gate above, so telling them the fix is safe and
			// far more actionable than a blind 500.
			replyError(w, http.StatusInternalServerError, encryptionNotConfiguredMsg)
			return
		}
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !encrypted {
		h.logger.Warn("webhook secret stored UNENCRYPTED at rest — set ENCRYPTION_KEY to encrypt (#1072)",
			"agent_id", agentID, "workspace_id", workspaceID)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents SET webhook_secret = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		storedSecret, now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("webhook secret rotate", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	// Exec never yields sql.ErrNoRows — a missing/soft-deleted/cross-tenant
	// agent shows up as a zero-row UPDATE instead.
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Audit the rotation without the secret value.
	h.logger.Info("webhook secret rotated",
		"agent_id", agentID, "workspace_id", workspaceID, "by_user", callerUserID)

	// The ONLY time the secret is ever returned.
	writeJSON(w, http.StatusOK, map[string]string{
		"webhook_secret": secret,
		"rotated_at":     now,
	})
}
