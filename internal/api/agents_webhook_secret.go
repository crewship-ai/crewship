package api

// Webhook signing-secret rotation (#999).
//
// The secret follows show-once semantics, mirroring CLI tokens and
// notification-channel secrets: no endpoint returns a stored secret back
// (the internal plaintext read was removed in the same change), so
// rotation is the ONLY way to obtain one. Mint new → return exactly once
// → the previous secret stops validating immediately.

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents SET webhook_secret = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		secret, now, agentID, workspaceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.Error("webhook secret rotate", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
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
