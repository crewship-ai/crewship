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
	Tags          []string `json:"tags"`
	AccountLabel  *string  `json:"account_label"`
	AccountEmail  *string  `json:"account_email"`
	RefreshToken  *string  `json:"refresh_token"`
	TokenExpires  *string  `json:"token_expires_at"`
	SecurityLevel *int     `json:"security_level"`
	// USERPASS: cleartext identifier half (e.g. "user@gmail.com").
	// Stored unencrypted in credentials.username because usernames are
	// identifiers, not secrets — mirrors Bitwarden's login.username
	// shape. The password lives in the existing encrypted Value field.
	Username *string `json:"username"`
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

	// MANAGER tier can create credentials — the FE CASL ability mirrors
	// this. "manage" was historically too tight (OWNER+ADMIN only) and
	// caused 403s for the Add flow even though the button rendered.
	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	// Defence in depth: authed middleware should always populate user,
	// but a future middleware reorder bug should not crash the write
	// path. Other call sites in this file already guard for nil; mirror
	// the pattern here.
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req createCredentialRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Name == "" || len(req.Name) < 1 || len(req.Name) > 255 {
		replyError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Value == "" && req.Type != "OAUTH2" {
		replyError(w, http.StatusBadRequest, "value is required")
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

	// Per-type validation (closed enum + USERPASS/SSH_KEY/CERTIFICATE
	// field requirements). Runs after the OAuth pending-value fixup so
	// the validator sees the same Value the DB will store.
	if msg := validateCredentialPayload(&req); msg != "" {
		replyError(w, http.StatusBadRequest, msg)
		return
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
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if !crewFound {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("Invalid crew_id: %s", cid))
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
		replyError(w, http.StatusInternalServerError, "Failed to encrypt credential")
		return
	}

	secLevel := 1
	if req.SecurityLevel != nil && *req.SecurityLevel >= 1 && *req.SecurityLevel <= 3 {
		secLevel = *req.SecurityLevel
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	credStatus := "ACTIVE"
	if oauthPending {
		credStatus = "PENDING"
	}

	var tagsArg any
	if encoded, ok := encodeTagsJSON(req.Tags); ok {
		tagsArg = encoded
	}

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, crew_id, account_label, account_email, username,
			token_expires_at, security_level, status, tags, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credID, workspaceID, req.Name, req.Description, encryptedValue,
		req.Type, req.Provider, req.Scope, legacyCrewID, req.AccountLabel, req.AccountEmail, req.Username,
		req.TokenExpires, secLevel, credStatus, tagsArg, user.ID, now, now)
	if err != nil {
		tx.Rollback()
		if strings.Contains(err.Error(), "UNIQUE") {
			replyError(w, http.StatusConflict, "Credential with this name already exists")
			return
		}
		h.logger.Error("insert credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
				replyError(w, http.StatusInternalServerError, "Internal server error")
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
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
		tx.Rollback()
		h.logger.Error("set credential crews", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Stamp the timeline so the detail-sheet Audit tab shows when the
	// credential first appeared. Outside the create tx — best-effort.
	if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventCreated, "", clientIP(r),
		map[string]any{"created_by": user.ID, "provider": req.Provider, "type": req.Type}); recErr != nil {
		// TODO(metrics): when an OpenTelemetry counter is wired into
		// this package, increment credential_audit_record_failures
		// here so ops can alarm on lost compliance events.
		h.logger.Warn("record CREATED audit event", "error", recErr, "credential_id", credID)
	}

	respCrewIDs := crewIDs
	if respCrewIDs == nil {
		respCrewIDs = []string{}
	}
	respTags := normaliseTags(req.Tags)
	if respTags == nil {
		respTags = []string{}
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
		Tags:         respTags,
		AccountLabel: req.AccountLabel,
		AccountEmail: req.AccountEmail,
		LastUsedIPs:  []string{},
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

	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	credFound, err := credentialExists(r.Context(), h.db, credID, workspaceID)
	if err != nil {
		h.logger.Error("credential exists check", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !credFound {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
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
		"security_level": "security_level", "username": "username",
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
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if !ok {
				replyError(w, http.StatusBadRequest, fmt.Sprintf("Invalid crew_id: %s", cid))
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
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if !crewFound {
				replyError(w, http.StatusBadRequest, "Invalid crew_id")
				return
			}
		}
	}

	ub := newUpdate()
	// valueRotated flips when this PATCH actually replaces the encrypted
	// secret — drives the post-commit audit ROTATE event so silent
	// in-place rewrites (the Vercel-style inline Save value flow) still
	// land on the timeline.
	valueRotated := false

	// Handle value separately (needs encryption)
	if val, ok := body["value"]; ok {
		if s, ok := val.(string); ok && s != "" {
			encrypted, err := encryption.Encrypt(s)
			if err != nil {
				h.logger.Error("encrypt credential value", "error", err)
				replyError(w, http.StatusInternalServerError, "Failed to encrypt credential")
				return
			}
			ub.Set("encrypted_value", encrypted)
			// Reset status when value changes so monitor re-validates
			ub.Set("status", "ACTIVE")
			ub.SetNull("last_error")
			valueRotated = true
		}
	}

	// Tags: accept either a JSON array or null. Empty/missing arrays
	// clear the column so the row goes back to NULL rather than "[]".
	if raw, ok := body["tags"]; ok {
		var tags []string
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					tags = append(tags, s)
				}
			}
		}
		if encoded, ok := encodeTagsJSON(tags); ok {
			ub.Set("tags", encoded)
		} else {
			ub.SetNull("tags")
		}
	}

	// security_level is the only typed scalar in `allowed`; mirror the
	// Create path's 1..3 validation so a PATCH can't smuggle a string
	// into the INTEGER column (SQLite stores by storage class but won't
	// reject the type mismatch).
	if raw, ok := body["security_level"]; ok {
		var n int
		switch v := raw.(type) {
		case float64:
			n = int(v)
		case int:
			n = v
		default:
			replyError(w, http.StatusBadRequest, "security_level must be 1, 2, or 3")
			return
		}
		if n < 1 || n > 3 {
			replyError(w, http.StatusBadRequest, "security_level must be 1, 2, or 3")
			return
		}
		body["security_level"] = n
	}

	for jsonKey, col := range allowed {
		if val, ok := body[jsonKey]; ok {
			ub.Set(col, val)
		}
	}

	if ub.Empty() && !updateCrewIDs {
		replyError(w, http.StatusBadRequest, "No fields to update")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx (update credential)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if !ub.Empty() {
		query, args := ub.Build("credentials", "id = ? AND workspace_id = ?", credID, workspaceID)
		if _, err := tx.ExecContext(r.Context(), query, args...); err != nil {
			tx.Rollback()
			h.logger.Error("update credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if updateCrewIDs {
		if err := h.setCrewIDs(r.Context(), tx, credID, crewIDs); err != nil {
			tx.Rollback()
			h.logger.Error("update credential crews", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit credential update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Inline value rewrite (Vercel-parity quick rotation) is logically
	// a rotation — no grace overlap, but the timeline must still see
	// it. Outside the tx so a slow audit insert never rolls back the
	// rotation itself.
	if valueRotated {
		var rotatedBy string
		if u := UserFromContext(r.Context()); u != nil {
			rotatedBy = u.ID
		}
		if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventRotate, "", clientIP(r),
			map[string]any{"mode": "inline", "rotated_by": rotatedBy}); recErr != nil {
			h.logger.Warn("record inline-rotate audit event", "error", recErr, "credential_id", credID)
		}
	}

	h.Get(w, r)
}

// Delete removes a credential and all its agent assignments.
// DELETE /api/v1/credentials/{credentialId}
