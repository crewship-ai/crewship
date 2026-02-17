package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
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
			continue
		}
		result = append(result, c)
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

	// Encrypt value using Go encryption (placeholder -- needs encryption.go port)
	encryptedValue := req.Value // TODO: port encryption from lib/encryption.ts

	_, err := h.db.ExecContext(r.Context(), `
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
