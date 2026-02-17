package api

import (
	"database/sql"
	"fmt"
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
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	Type           string  `json:"type"`
	Provider       string  `json:"provider"`
	Status         string  `json:"status"`
	Scope          string  `json:"scope"`
	CrewID         *string `json:"crew_id"`
	AccountLabel   *string `json:"account_label"`
	AccountEmail   *string `json:"account_email"`
	TokenExpiresAt *string `json:"token_expires_at"`
	LastCheckedAt  *string `json:"last_checked_at"`
	LastError      *string `json:"last_error"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	AgentCount     int     `json:"_count_agent_credentials"`
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
	writeJSON(w, http.StatusOK, result)
}

type createCredentialRequest struct {
	Name          string  `json:"name"`
	Description   *string `json:"description"`
	Value         string  `json:"value"`
	Type          string  `json:"type"`
	Provider      string  `json:"provider"`
	Scope         string  `json:"scope"`
	CrewID        *string `json:"crew_id"`
	AccountLabel  *string `json:"account_label"`
	AccountEmail  *string `json:"account_email"`
	RefreshToken  *string `json:"refresh_token"`
	TokenExpires  *string `json:"token_expires_at"`
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
	if req.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value is required"})
		return
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

	if req.CrewID != nil {
		var crewExists string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
			*req.CrewID, workspaceID).Scan(&crewExists)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid crew_id"})
			return
		}
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

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO credentials (id, workspace_id, name, description, encrypted_value,
			type, provider, scope, crew_id, account_label, account_email,
			token_expires_at, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credID, workspaceID, req.Name, req.Description, encryptedValue,
		req.Type, req.Provider, req.Scope, req.CrewID, req.AccountLabel, req.AccountEmail,
		req.TokenExpires, user.ID, now, now)
	if err != nil {
		h.logger.Error("insert credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, credentialResponse{
		ID:           credID,
		Name:         req.Name,
		Description:  req.Description,
		Type:         req.Type,
		Provider:     req.Provider,
		Status:       "ACTIVE",
		Scope:        req.Scope,
		CrewID:       req.CrewID,
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
	}

	if crewIDVal, ok := body["crew_id"]; ok && crewIDVal != nil {
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
		}
	}

	for jsonKey, col := range allowed {
		if val, ok := body[jsonKey]; ok {
			setClauses = append(setClauses, col+" = ?")
			args = append(args, val)
		}
	}

	if len(setClauses) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, now, credID, workspaceID)

	query := fmt.Sprintf("UPDATE credentials SET %s WHERE id = ? AND workspace_id = ?", strings.Join(setClauses, ", "))
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update credential", "error", err)
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
