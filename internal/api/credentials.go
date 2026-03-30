package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

type CredentialHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCredentialHandler(db *sql.DB, logger *slog.Logger) *CredentialHandler {
	return &CredentialHandler{db: db, logger: logger}
}

type credentialResponse struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    *string  `json:"description"`
	Type           string   `json:"type"`
	Provider       string   `json:"provider"`
	Status         string   `json:"status"`
	Scope          string   `json:"scope"`
	CrewID         *string  `json:"crew_id"`
	CrewIDs        []string `json:"crew_ids"`
	AccountLabel   *string  `json:"account_label"`
	AccountEmail   *string  `json:"account_email"`
	TokenExpiresAt *string  `json:"token_expires_at"`
	LastCheckedAt  *string  `json:"last_checked_at"`
	LastError      *string  `json:"last_error"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	AgentCount     int      `json:"_count_agent_credentials"`
}

// loadCrewIDs fetches crew_ids from the junction table for a single credential.
func (h *CredentialHandler) loadCrewIDs(ctx context.Context, credentialID string) []string {
	rows, err := h.db.QueryContext(ctx,
		"SELECT crew_id FROM credential_crews WHERE credential_id = ? ORDER BY created_at", credentialID)
	if err != nil {
		return []string{}
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// loadCrewIDsBatch fetches crew_ids for multiple credentials in one query.
func (h *CredentialHandler) loadCrewIDsBatch(ctx context.Context, credentialIDs []string) map[string][]string {
	result := make(map[string][]string, len(credentialIDs))
	if len(credentialIDs) == 0 {
		return result
	}
	placeholders := make([]string, len(credentialIDs))
	args := make([]interface{}, len(credentialIDs))
	for i, id := range credentialIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := h.db.QueryContext(ctx,
		"SELECT credential_id, crew_id FROM credential_crews WHERE credential_id IN ("+strings.Join(placeholders, ",")+") ORDER BY created_at",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var credID, crewID string
		if rows.Scan(&credID, &crewID) == nil {
			result[credID] = append(result[credID], crewID)
		}
	}
	return result
}

// setCrewIDs replaces crew_ids for a credential in the junction table.
func (h *CredentialHandler) setCrewIDs(ctx context.Context, tx *sql.Tx, credentialID string, crewIDs []string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM credential_crews WHERE credential_id = ?", credentialID); err != nil {
		return err
	}
	for _, crewID := range crewIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO credential_crews (credential_id, crew_id) VALUES (?, ?)",
			credentialID, crewID); err != nil {
			return err
		}
	}
	return nil
}

func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.name, c.description, c.type, c.provider, c.status,
			c.scope, c.crew_id, c.account_label, c.account_email,
			c.token_expires_at, c.last_checked_at, c.last_error,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agent_credentials WHERE credential_id = c.id) AS agent_count
		FROM credentials c
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		ORDER BY c.type ASC, c.created_at DESC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []credentialResponse
	for rows.Next() {
		var c credentialResponse
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
			&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail,
			&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
			&c.CreatedAt, &c.UpdatedAt, &c.AgentCount); err != nil {
			h.logger.Error("scan credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []credentialResponse{}
	}

	// Batch-load crew_ids from junction table
	credIDs := make([]string, len(result))
	for i, c := range result {
		credIDs[i] = c.ID
	}
	crewIDsMap := h.loadCrewIDsBatch(r.Context(), credIDs)
	for i := range result {
		if ids, ok := crewIDsMap[result[i].ID]; ok {
			result[i].CrewIDs = ids
		} else {
			result[i].CrewIDs = []string{}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

type createCredentialRequest struct {
	Name          string   `json:"name"`
	Description   *string  `json:"description"`
	Value         string   `json:"value"`
	Type          string   `json:"type"`
	Provider      string   `json:"provider"`
	Scope         string   `json:"scope"`
	CrewID        *string  `json:"crew_id"`
	CrewIDs       []string `json:"crew_ids"`
	AccountLabel  *string  `json:"account_label"`
	AccountEmail  *string  `json:"account_email"`
	RefreshToken  *string  `json:"refresh_token"`
	TokenExpires  *string  `json:"token_expires_at"`
	SecurityLevel *int     `json:"security_level"`
	// OAuth 2.0 fields (used when type = OAUTH2)
	OAuthClientID     *string `json:"oauth_client_id"`
	OAuthClientSecret *string `json:"oauth_client_secret"`
	OAuthAuthURL      *string `json:"oauth_auth_url"`
	OAuthTokenURL     *string `json:"oauth_token_url"`
	OAuthScopes       *string `json:"oauth_scopes"`
}

func (h *CredentialHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req createCredentialRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name == "" || len(req.Name) < 1 || len(req.Name) > 255 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.Value == "" && req.Type != "OAUTH2" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value is required"})
		return
	}
	if req.Value == "" {
		req.Value = "pending_oauth" // placeholder until OAuth flow completes
	}

	if req.Type == "" {
		req.Type = "SECRET"
	}
	if req.Provider == "" {
		req.Provider = "NONE"
	}
	if req.Scope == "" {
		req.Scope = "WORKSPACE"
	}

	// Merge crew_ids and legacy crew_id into a single list
	crewIDs := req.CrewIDs
	if req.CrewID != nil && *req.CrewID != "" {
		found := false
		for _, id := range crewIDs {
			if id == *req.CrewID {
				found = true
				break
			}
		}
		if !found {
			crewIDs = append(crewIDs, *req.CrewID)
		}
	}

	// Auto-set scope when crews are provided
	if len(crewIDs) > 0 {
		req.Scope = "CREW"
	}

	// Validate all crew IDs
	for _, cid := range crewIDs {
		var crewExists string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
			cid, workspaceID).Scan(&crewExists)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Invalid crew_id: %s", cid)})
			return
		}
	}

	// Keep legacy crew_id field pointing to first crew for backwards compat
	var legacyCrewID *string
	if len(crewIDs) > 0 {
		legacyCrewID = &crewIDs[0]
	}

	// Remove soft-deleted credential with same name
	h.db.ExecContext(r.Context(),
		"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
		workspaceID, req.Name)

	now := time.Now().UTC().Format(time.RFC3339)
	credID := generateCUID()

	encryptedValue, err := encryption.Encrypt(req.Value)
	if err != nil {
		h.logger.Error("encrypt credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
		return
	}

	secLevel := 1
	if req.SecurityLevel != nil && *req.SecurityLevel >= 1 && *req.SecurityLevel <= 3 {
		secLevel = *req.SecurityLevel
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, crew_id, account_label, account_email,
			token_expires_at, security_level, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credID, workspaceID, req.Name, req.Description, encryptedValue,
		req.Type, req.Provider, req.Scope, legacyCrewID, req.AccountLabel, req.AccountEmail,
		req.TokenExpires, secLevel, user.ID, now, now)
	if err != nil {
		tx.Rollback()
		h.logger.Error("insert credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Store OAuth fields if type is OAUTH2
	if req.Type == "OAUTH2" && req.OAuthClientID != nil {
		var encClientSecret string
		if req.OAuthClientSecret != nil && *req.OAuthClientSecret != "" {
			encClientSecret, err = encryption.Encrypt(*req.OAuthClientSecret)
			if err != nil {
				tx.Rollback()
				h.logger.Error("encrypt OAuth client secret", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
		}
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE credentials SET oauth_client_id = ?, oauth_client_secret_enc = ?,
				oauth_auth_url = ?, oauth_token_url = ?, oauth_scopes = ?
			WHERE id = ?`,
			req.OAuthClientID, encClientSecret,
			req.OAuthAuthURL, req.OAuthTokenURL, req.OAuthScopes,
			credID); err != nil {
			tx.Rollback()
			h.logger.Error("store OAuth fields", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
		tx.Rollback()
		h.logger.Error("set credential crews", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	respCrewIDs := crewIDs
	if respCrewIDs == nil {
		respCrewIDs = []string{}
	}

	writeJSON(w, http.StatusCreated, credentialResponse{
		ID:           credID,
		Name:         req.Name,
		Description:  req.Description,
		Type:         req.Type,
		Provider:     req.Provider,
		Status:       "ACTIVE",
		Scope:        req.Scope,
		CrewID:       legacyCrewID,
		CrewIDs:      respCrewIDs,
		AccountLabel: req.AccountLabel,
		AccountEmail: req.AccountEmail,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}

func (h *CredentialHandler) Get(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var c credentialResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.name, c.description, c.type, c.provider, c.status,
			c.scope, c.crew_id, c.account_label, c.account_email,
			c.token_expires_at, c.last_checked_at, c.last_error,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agent_credentials WHERE credential_id = c.id) AS agent_count
		FROM credentials c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
	`, credID, workspaceID).Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
		&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail,
		&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
		&c.CreatedAt, &c.UpdatedAt, &c.AgentCount)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
			return
		}
		h.logger.Error("get credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	c.CrewIDs = h.loadCrewIDs(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, c)
}

func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		credID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	allowed := map[string]string{
		"name": "name", "description": "description", "type": "type",
		"provider": "provider", "status": "status", "scope": "scope",
		"crew_id": "crew_id", "account_label": "account_label",
		"account_email": "account_email", "token_expires_at": "token_expires_at",
		"security_level": "security_level",
	}

	// Parse crew_ids if provided — will be written to junction table
	var updateCrewIDs bool
	var crewIDs []string
	if raw, ok := body["crew_ids"]; ok {
		updateCrewIDs = true
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok && s != "" {
					crewIDs = append(crewIDs, s)
				}
			}
		}
		// Validate all crew IDs
		for _, cid := range crewIDs {
			var crewExists string
			err := h.db.QueryRowContext(r.Context(),
				"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
				cid, workspaceID).Scan(&crewExists)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Invalid crew_id: %s", cid)})
				return
			}
		}
		// Auto-update scope and legacy crew_id
		if len(crewIDs) > 0 {
			body["scope"] = "CREW"
			body["crew_id"] = crewIDs[0]
		} else {
			body["scope"] = "WORKSPACE"
			body["crew_id"] = nil
		}
		delete(body, "crew_ids")
	} else if crewIDVal, ok := body["crew_id"]; ok && crewIDVal != nil {
		if crewIDStr, ok := crewIDVal.(string); ok && crewIDStr != "" {
			var crewExists string
			err := h.db.QueryRowContext(r.Context(),
				"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
				crewIDStr, workspaceID).Scan(&crewExists)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid crew_id"})
				return
			}
		}
	}

	var setClauses []string
	var args []interface{}

	// Handle value separately (needs encryption)
	if val, ok := body["value"]; ok {
		if s, ok := val.(string); ok && s != "" {
			encrypted, err := encryption.Encrypt(s)
			if err != nil {
				h.logger.Error("encrypt credential value", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
				return
			}
			setClauses = append(setClauses, "encrypted_value = ?")
			args = append(args, encrypted)
			// Reset status when value changes so monitor re-validates
			setClauses = append(setClauses, "status = ?", "last_error = ?")
			args = append(args, "ACTIVE", nil)
		}
	}

	for jsonKey, col := range allowed {
		if val, ok := body[jsonKey]; ok {
			setClauses = append(setClauses, col+" = ?")
			args = append(args, val)
		}
	}

	if len(setClauses) == 0 && !updateCrewIDs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx (update credential)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if len(setClauses) > 0 {
		setClauses = append(setClauses, "updated_at = ?")
		args = append(args, now, credID, workspaceID)

		query := fmt.Sprintf("UPDATE credentials SET %s WHERE id = ? AND workspace_id = ?", strings.Join(setClauses, ", "))
		if _, err := tx.ExecContext(r.Context(), query, args...); err != nil {
			tx.Rollback()
			h.logger.Error("update credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	if updateCrewIDs {
		if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
			tx.Rollback()
			h.logger.Error("update credential crews", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential update", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.Get(w, r)
}

func (h *CredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE credentials SET deleted_at = ? WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		now, credID, workspaceID)
	if err != nil {
		h.logger.Error("delete credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Test validates a credential value against the provider's API without storing it.
// POST /api/v1/credentials/test
func (h *CredentialHandler) Test(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Type     string `json:"type"`
		Value    string `json:"value"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if body.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Value is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	type testResult struct {
		Valid   bool   `json:"valid"`
		Status int    `json:"status"`
		Error  string `json:"error,omitempty"`
	}

	switch body.Provider {
	case "ANTHROPIC":
		// OAuth setup tokens (sk-ant-oat*) cannot be validated via standard API.
		// They only work inside Claude Code's authenticated tunnel.
		if body.Type == "AI_CLI_TOKEN" || isAnthropicOAuthToken(body.Value) {
			writeJSON(w, http.StatusOK, testResult{Valid: true, Error: "OAuth token accepted (cannot validate via API, will be verified at runtime)"})
			return
		}

		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/v1/models", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("x-api-key", body.Value)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid API key"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Access revoked"})
		case http.StatusTooManyRequests:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode, Error: "Rate limited (key is valid but temporarily throttled)"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "OPENAI":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		if resp.StatusCode == http.StatusOK {
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		} else if resp.StatusCode == http.StatusUnauthorized {
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid API key"})
		} else {
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GOOGLE":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://generativelanguage.googleapis.com/v1/models?key="+body.Value, nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		if resp.StatusCode == http.StatusOK {
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		} else {
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GITHUB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)
		req.Header.Set("User-Agent", "Crewship/1.0")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "GITLAB":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://gitlab.com/api/v4/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("PRIVATE-TOKEN", body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		case http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Token lacks required scopes"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	case "VERCEL":
		req, err := http.NewRequestWithContext(ctx, "GET", "https://api.vercel.com/v2/user", nil)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Failed to create request"})
			return
		}
		req.Header.Set("Authorization", "Bearer "+body.Value)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, testResult{Error: "Connection failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)

		switch resp.StatusCode {
		case http.StatusOK:
			writeJSON(w, http.StatusOK, testResult{Valid: true, Status: resp.StatusCode})
		case http.StatusUnauthorized, http.StatusForbidden:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: "Invalid token"})
		default:
			writeJSON(w, http.StatusOK, testResult{Status: resp.StatusCode, Error: fmt.Sprintf("Unexpected response: %d", resp.StatusCode)})
		}

	default:
		writeJSON(w, http.StatusOK, testResult{Valid: true, Error: "No validation available for this provider"})
	}
}

// DefaultEnvVar returns the conventional env var name for a CLI tool provider.
// GET /api/v1/credentials/default-env-var?provider=GITHUB
func (h *CredentialHandler) DefaultEnvVar(w http.ResponseWriter, r *http.Request) {
	prov := r.URL.Query().Get("provider")
	envVar := defaultEnvVarForCLIProvider(prov)
	writeJSON(w, http.StatusOK, map[string]string{"env_var": envVar})
}

func defaultEnvVarForCLIProvider(provider string) string {
	switch provider {
	case "GITHUB":
		return "GH_TOKEN"
	case "GITLAB":
		return "GITLAB_TOKEN"
	case "VERCEL":
		return "VERCEL_TOKEN"
	case "AWS":
		return "AWS_ACCESS_KEY_ID"
	case "KUBERNETES":
		return "KUBECONFIG"
	default:
		return ""
	}
}

// isAnthropicOAuthToken detects if a value is an Anthropic OAuth/setup token
// rather than a plain API key. OAuth tokens use "sk-ant-oat" prefix.
func isAnthropicOAuthToken(value string) bool {
	return strings.HasPrefix(value, "sk-ant-oat")
}
