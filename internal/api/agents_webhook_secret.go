package api

// Webhook signing-secret rotation (#999).
//
// The secret follows show-once semantics, mirroring CLI tokens and
// notification-channel secrets: no endpoint returns a stored secret back
// (the internal plaintext read was removed in the same change), so
// rotation is the ONLY way to obtain one. Mint new → return exactly once
// → the previous secret stops validating immediately.

import (
	"encoding/json"
	"errors"
	"io"
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

// Overlap window bounds for a rotated agent webhook secret, in seconds.
// 0 (the default) keeps the historic behaviour: the old secret dies at once.
const (
	agentWebhookOverlapMinSeconds = 60
	agentWebhookOverlapMaxSeconds = 7 * 24 * 3600
)

type rotateWebhookSecretRequest struct {
	// PreviousSecretTTLSeconds keeps the secret this rotation replaces valid
	// for this long: 0 (default) or 60..604800.
	PreviousSecretTTLSeconds int `json:"previous_secret_ttl_seconds"`
}

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

	var req rotateWebhookSecretRequest
	raw, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if rerr != nil {
		replyError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			replyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	ttl := req.PreviousSecretTTLSeconds
	if ttl != 0 && (ttl < agentWebhookOverlapMinSeconds || ttl > agentWebhookOverlapMaxSeconds) {
		replyError(w, http.StatusBadRequest, "previous_secret_ttl_seconds must be 0 or between 60 and 604800 (7 days)")
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
	nowT := time.Now().UTC()
	now := nowT.Format(time.RFC3339Nano)
	// The replaced secret becomes the previous one only when an overlap was
	// asked for and a secret existed. The UPDATE reads the OLD column values on
	// its right-hand side, so the swap is atomic, and a rotation during an open
	// overlap overwrites the previous slot: at most two secrets are ever live.
	var prevExpires any
	if ttl > 0 {
		prevExpires = nowT.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339Nano)
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents SET
			previous_webhook_secret = CASE WHEN ? IS NOT NULL AND webhook_secret IS NOT NULL AND webhook_secret != '' THEN webhook_secret ELSE NULL END,
			previous_webhook_secret_expires_at = CASE WHEN ? IS NOT NULL AND webhook_secret IS NOT NULL AND webhook_secret != '' THEN ? ELSE NULL END,
			webhook_secret = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		prevExpires, prevExpires, prevExpires, storedSecret, now, agentID, workspaceID)
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

	resp := map[string]string{
		"webhook_secret": secret,
		"rotated_at":     now,
	}
	expiresAt := ""
	if ttl > 0 {
		expiresAt = prevExpires.(string)
		resp["previous_secret_expires_at"] = expiresAt
	}
	auditFromRequest(r, h.db, "agent.webhook_secret.rotated", "AGENT", agentID,
		map[string]interface{}{"overlap": ttl > 0, "previous_secret_expires_at": expiresAt})

	// The ONLY time the secret is ever returned.
	writeJSON(w, http.StatusOK, resp)
}

// RevokePreviousWebhookSecret ends an open rotation overlap now, so only the
// current secret verifies. Idempotent: with no overlap open it still succeeds.
// DELETE /api/v1/agents/{agentId}/webhook-secret/previous
func (h *AgentHandler) RevokePreviousWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	callerUserID := ""
	if user := UserFromContext(r.Context()); user != nil {
		callerUserID = user.ID
	}
	ok, err := canEditAgent(r.Context(), h.db, callerUserID, role, agentID)
	if err != nil {
		h.logger.Error("webhook secret revoke gate", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !ok {
		replyForbidden(w, h.logger, callerUserID, role,
			"agent.webhook_secret.revoke_previous", "agent:"+agentID)
		return
	}
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents SET previous_webhook_secret = NULL, previous_webhook_secret_expires_at = NULL, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), agentID, workspaceID)
	if err != nil {
		h.logger.Error("webhook previous secret revoke", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	auditFromRequest(r, h.db, "agent.webhook_previous_secret.revoked", "AGENT", agentID, nil)
	w.WriteHeader(http.StatusNoContent)
}
