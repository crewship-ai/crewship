package api

// Credential write paths — Create + Update. Each carries provider-
// specific validation and encrypted-storage setup, so they're large
// enough to deserve their own file. Extracted from credentials.go.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

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

// Create stores a new encrypted credential in the workspace.
// POST /api/v1/credentials

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
	oauthPending := false
	if req.Value == "" && req.Type == "OAUTH2" {
		req.Value = "pending_oauth" // placeholder until OAuth flow completes
		oauthPending = true
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
		crewFound, err := crewExists(r.Context(), h.db, cid, workspaceID)
		if err != nil {
			h.logger.Error("crew exists check", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if !crewFound {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("Invalid crew_id: %s", cid)})
			return
		}
	}

	// Keep legacy crew_id field pointing to first crew for backwards compat
	var legacyCrewID *string
	if len(crewIDs) > 0 {
		legacyCrewID = &crewIDs[0]
	}

	// Remove soft-deleted credential with same name so the INSERT doesn't hit a unique constraint.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM credentials WHERE workspace_id = ? AND name = ? AND deleted_at IS NOT NULL",
		workspaceID, req.Name); err != nil {
		h.logger.Warn("cleanup soft-deleted credential", "name", req.Name, "error", err)
	}

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

	credStatus := "ACTIVE"
	if oauthPending {
		credStatus = "PENDING"
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, crew_id, account_label, account_email,
			token_expires_at, security_level, status, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credID, workspaceID, req.Name, req.Description, encryptedValue,
		req.Type, req.Provider, req.Scope, legacyCrewID, req.AccountLabel, req.AccountEmail,
		req.TokenExpires, secLevel, credStatus, user.ID, now, now)
	if err != nil {
		tx.Rollback()
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Credential with this name already exists"})
			return
		}
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
		AgentNames:   []string{},
	})
}

// Get returns a single credential by ID (without the secret value).
// GET /api/v1/credentials/{credentialId}

func (h *CredentialHandler) Update(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	credFound, err := credentialExists(r.Context(), h.db, credID, workspaceID)
	if err != nil {
		h.logger.Error("credential exists check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !credFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
		return
	}

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Note: "status" is intentionally excluded to prevent users from
	// re-activating revoked/expired credentials. Status changes are
	// managed by the credential monitor and OAuth refresh worker.
	allowed := map[string]string{
		"name": "name", "description": "description", "type": "type",
		"provider": "provider", "scope": "scope",
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
			ok, err := crewExists(r.Context(), h.db, cid, workspaceID)
			if err != nil {
				h.logger.Error("crew exists check", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
			if !ok {
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
			crewFound, err := crewExists(r.Context(), h.db, crewIDStr, workspaceID)
			if err != nil {
				h.logger.Error("crew exists check", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
			if !crewFound {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid crew_id"})
				return
			}
		}
	}

	ub := newUpdate()

	// Handle value separately (needs encryption)
	if val, ok := body["value"]; ok {
		if s, ok := val.(string); ok && s != "" {
			encrypted, err := encryption.Encrypt(s)
			if err != nil {
				h.logger.Error("encrypt credential value", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to encrypt credential"})
				return
			}
			ub.Set("encrypted_value", encrypted)
			// Reset status when value changes so monitor re-validates
			ub.Set("status", "ACTIVE")
			ub.SetNull("last_error")
		}
	}

	for jsonKey, col := range allowed {
		if val, ok := body[jsonKey]; ok {
			ub.Set(col, val)
		}
	}

	if ub.Empty() && !updateCrewIDs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx (update credential)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if !ub.Empty() {
		query, args := ub.Build("credentials", "id = ? AND workspace_id = ?", credID, workspaceID)
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

// Delete removes a credential and all its agent assignments.
// DELETE /api/v1/credentials/{credentialId}
