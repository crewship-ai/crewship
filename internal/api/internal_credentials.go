package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// sidecarUseDebounce throttles sidecar-driven USE events so the audit
// timeline doesn't fill with one row per poll cycle. The sidecar fetches
// credentials whenever an agent is about to consume them; a 60s window
// is long enough to merge bursts (one agent run usually fetches each
// credential once at start, maybe again on rotate) and short enough to
// catch independent runs.
const sidecarUseDebounce = 60 * time.Second

// maybeRecordSidecarUse emits an AuditEventUse for credID iff the most
// recent USE event for the same credential is older than
// sidecarUseDebounce. Errors are logged but never bubbled — audit
// failure must not block the credential fetch the sidecar is doing.
//
// Closes the gap noted in F-3 of the credentials/scrubber pentest agent:
// the audit timeline previously showed only manual Test/Rotate/Revoke,
// not the actual sidecar-driven USE that's what an incident responder
// most cares about ("when was this credential last touched?").
func maybeRecordSidecarUse(ctx context.Context, db *sql.DB, logger *slog.Logger, credID string, ip string) {
	if credID == "" {
		return
	}
	var lastUsed sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT last_used_at FROM credentials WHERE id = ?`, credID).Scan(&lastUsed); err != nil {
		// Lookup failure shouldn't block the fetch. Log + record best-effort.
		if logger != nil {
			logger.Debug("sidecar use audit: lookup last_used_at", "credential_id", credID, "error", err)
		}
	} else if lastUsed.Valid {
		if t, err := time.Parse(time.RFC3339, lastUsed.String); err == nil {
			if time.Since(t) < sidecarUseDebounce {
				return // debounced — recent enough
			}
		}
	}
	if err := RecordCredentialEvent(ctx, db, logger, credID, AuditEventUse, "" /* agent unknown at this layer */, ip, map[string]any{"source": "sidecar_fetch"}); err != nil {
		if logger != nil {
			logger.Warn("sidecar use audit: record failed", "credential_id", credID, "error", err)
		}
	}
}

// ListCredentials returns active credentials for the sidecar, with decrypted values.
// GET /api/v1/internal/credentials — called by sidecar to inject secrets into agent environments.
func (h *InternalHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	provider := r.URL.Query().Get("provider")

	query := `SELECT id, workspace_id, name, type, provider, encrypted_value,
		encrypted_refresh_token, token_expires_at, account_label, account_email, status
		FROM credentials
		WHERE status IN ('ACTIVE', 'EXPIRED', 'ERROR') AND deleted_at IS NULL
		AND type IN ('AI_CLI_TOKEN', 'API_KEY') AND provider != 'NONE'`

	var args []any
	if workspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, workspaceID)
	}
	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY type ASC, created_at ASC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("internal list credentials", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	type credResult struct {
		ID           string  `json:"id"`
		WorkspaceID  string  `json:"workspace_id"`
		Name         string  `json:"name"`
		Type         string  `json:"type"`
		Provider     string  `json:"provider"`
		AccessToken  string  `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		TokenExpires *string `json:"token_expires_at"`
		AccountLabel *string `json:"account_label"`
		Status       string  `json:"status"`
	}

	var result []credResult
	for rows.Next() {
		var c credResult
		var encValue string
		var encRefresh, accountEmail sql.NullString
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Type, &c.Provider,
			&encValue, &encRefresh, &c.TokenExpires, &c.AccountLabel, &accountEmail, &c.Status); err != nil {
			h.logger.Error("scan internal credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		decrypted, err := encryption.Decrypt(encValue)
		if err != nil {
			h.logger.Error("decrypt credential", "id", c.ID, "error", err)
			continue
		}
		c.AccessToken = decrypted
		if encRefresh.Valid {
			rt, err := encryption.Decrypt(encRefresh.String)
			if err != nil {
				h.logger.Debug("decrypt refresh token", "id", c.ID, "error", err)
			} else {
				c.RefreshToken = &rt
			}
		}
		// Best-effort USE audit. Empty IP is fine — internal callers are
		// the sidecar (loopback) and the IP would always be 127.0.0.1
		// which is no signal. Debounced to one row per credential per
		// minute so a busy sidecar doesn't flood the timeline.
		maybeRecordSidecarUse(r.Context(), h.db, h.logger, c.ID, "")
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (internal credentials)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if result == nil {
		result = []credResult{}
	}
	writeJSON(w, http.StatusOK, result)
}

// UpdateCredentialStatus updates the health status of a credential after the sidecar validates it.
// PATCH /api/v1/internal/credentials/{credentialId}/status
func (h *InternalHandler) UpdateCredentialStatus(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := r.URL.Query().Get("workspace_id") // optional for backwards compat

	var body struct {
		Status       string  `json:"status"`
		LastError    *string `json:"last_error"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		TokenExpires *string `json:"token_expires_at"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	validStatuses := map[string]bool{
		"ACTIVE": true, "EXPIRED": true, "RATE_LIMITED": true, "REVOKED": true, "ERROR": true,
	}
	if !validStatuses[body.Status] {
		replyError(w, http.StatusBadRequest, "Invalid status")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Build WHERE clause — workspace_id is optional (internal callers may not send it)
	where := "id = ?"
	whereArgs := []any{credID}
	if workspaceID != "" {
		where += " AND workspace_id = ?"
		whereArgs = append(whereArgs, workspaceID)
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(r.Context(),
		"UPDATE credentials SET status = ?, last_checked_at = ?, updated_at = ? WHERE "+where,
		append([]any{body.Status, now, now}, whereArgs...)...)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		h.logger.Error("update credential rows affected", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	if body.LastError != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET last_error = ? WHERE "+where, append([]any{*body.LastError}, whereArgs...)...); err != nil {
			h.logger.Error("update credential last_error", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.AccessToken != nil {
		enc, err := encryption.Encrypt(*body.AccessToken)
		if err != nil {
			h.logger.Error("encrypt access token", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to encrypt token")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_value = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential access token", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.RefreshToken != nil {
		enc, err := encryption.Encrypt(*body.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to encrypt token")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_refresh_token = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential refresh token", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.TokenExpires != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET token_expires_at = ? WHERE "+where, append([]any{*body.TokenExpires}, whereArgs...)...); err != nil {
			h.logger.Error("update credential token_expires_at", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": credID, "status": body.Status, "last_checked_at": now})
}

// GetWebhookSecret returns the webhook secret for a given agent, used by the sidecar for signature validation.
// GET /api/v1/internal/agents/{agentId}/webhook-secret
func (h *InternalHandler) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	var secret sql.NullString
	err := h.db.QueryRowContext(r.Context(), "SELECT webhook_secret FROM agents WHERE id = ?", agentID).Scan(&secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("webhook secret lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !secret.Valid || secret.String == "" {
		replyError(w, http.StatusNotFound, "Webhook secret not configured for agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret.String})
}
