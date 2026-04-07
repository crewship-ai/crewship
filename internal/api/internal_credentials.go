package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (internal credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []credResult{}
	}
	writeJSON(w, http.StatusOK, result)
}

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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	validStatuses := map[string]bool{
		"ACTIVE": true, "EXPIRED": true, "RATE_LIMITED": true, "REVOKED": true, "ERROR": true,
	}
	if !validStatuses[body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid status"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(r.Context(),
		"UPDATE credentials SET status = ?, last_checked_at = ?, updated_at = ? WHERE "+where,
		append([]any{body.Status, now, now}, whereArgs...)...)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		h.logger.Error("update credential rows affected", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	if body.LastError != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET last_error = ? WHERE "+where, append([]any{*body.LastError}, whereArgs...)...); err != nil {
			h.logger.Error("update credential last_error", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.AccessToken != nil {
		enc, err := encryption.Encrypt(*body.AccessToken)
		if err != nil {
			h.logger.Error("encrypt access token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt token"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_value = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential access token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.RefreshToken != nil {
		enc, err := encryption.Encrypt(*body.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt token"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_refresh_token = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential refresh token", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}
	if body.TokenExpires != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET token_expires_at = ? WHERE "+where, append([]any{*body.TokenExpires}, whereArgs...)...); err != nil {
			h.logger.Error("update credential token_expires_at", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": credID, "status": body.Status, "last_checked_at": now})
}

func (h *InternalHandler) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	var secret sql.NullString
	err := h.db.QueryRowContext(r.Context(), "SELECT webhook_secret FROM agents WHERE id = ?", agentID).Scan(&secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("webhook secret lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !secret.Valid || secret.String == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Webhook secret not configured for agent"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret.String})
}
