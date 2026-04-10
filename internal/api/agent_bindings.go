package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// --- Response types ---

type agentMCPBindingResponse struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	MCPServerID      string  `json:"mcp_server_id"`
	MCPServerScope   string  `json:"mcp_server_scope"`
	CredentialID     *string `json:"credential_id"`
	CredType         *string `json:"cred_type"`
	CredHeader       *string `json:"cred_header"`
	Enabled          bool    `json:"enabled"`
	ConfigOverride   *string `json:"config_override_json"`
	CreatedAt        string  `json:"created_at"`
	ServerName       string  `json:"server_name"`
	ServerDisplay    string  `json:"server_display_name"`
	CredentialName   *string `json:"credential_name"`
}

// --- Request types ---

type createAgentBindingRequest struct {
	MCPServerID    string  `json:"mcp_server_id"`
	MCPServerScope string  `json:"mcp_server_scope"`
	CredentialID   *string `json:"credential_id"`
	CredType       *string `json:"cred_type"`      // "bearer", "api_key", "basic"
	CredHeader     *string `json:"cred_header"`     // custom header for api_key type
	EnvVarName     *string `json:"env_var_name"`    // env var name for stdio credential injection
	Enabled        *bool   `json:"enabled"`
	ConfigOverride *string `json:"config_override_json"`
}

type updateAgentBindingRequest struct {
	CredentialID   *string `json:"credential_id"`
	CredType       *string `json:"cred_type"`
	CredHeader     *string `json:"cred_header"`
	EnvVarName     *string `json:"env_var_name"`
	Enabled        *bool   `json:"enabled"`
	ConfigOverride *string `json:"config_override_json"`
}

// ==========================================
// Agent MCP Bindings
// ==========================================

// ListAgentBindings returns all MCP server bindings for a given agent.
// GET /api/v1/agents/{agentId}/mcp-bindings
func (h *IntegrationHandler) ListAgentBindings(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")

	// Verify agent
	if err := agentExists(r.Context(), h.db, agentID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent existence", "error", err, "agent_id", agentID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT b.id, b.agent_id, b.mcp_server_id, b.mcp_server_scope,
			b.credential_id, b.cred_type, b.cred_header, b.enabled, b.config_override_json, b.created_at,
			CASE
				WHEN b.mcp_server_scope = 'workspace' THEN COALESCE(ws.name, '')
				WHEN b.mcp_server_scope = 'crew' THEN COALESCE(cs.name, '')
			END AS server_name,
			CASE
				WHEN b.mcp_server_scope = 'workspace' THEN COALESCE(ws.display_name, '')
				WHEN b.mcp_server_scope = 'crew' THEN COALESCE(cs.display_name, '')
			END AS server_display,
			c.name AS credential_name
		FROM agent_mcp_bindings b
		LEFT JOIN workspace_mcp_servers ws ON b.mcp_server_id = ws.id AND b.mcp_server_scope = 'workspace'
		LEFT JOIN crew_mcp_servers cs ON b.mcp_server_id = cs.id AND b.mcp_server_scope = 'crew'
		LEFT JOIN credentials c ON b.credential_id = c.id
		WHERE b.agent_id = ?
		ORDER BY b.created_at DESC`, agentID)
	if err != nil {
		h.logger.Error("list agent bindings", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []agentMCPBindingResponse
	for rows.Next() {
		var b agentMCPBindingResponse
		var enabled int
		if err := rows.Scan(&b.ID, &b.AgentID, &b.MCPServerID, &b.MCPServerScope,
			&b.CredentialID, &b.CredType, &b.CredHeader, &enabled, &b.ConfigOverride, &b.CreatedAt,
			&b.ServerName, &b.ServerDisplay, &b.CredentialName); err != nil {
			h.logger.Error("scan agent binding", "error", err)
			continue
		}
		b.Enabled = enabled == 1
		results = append(results, b)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate agent bindings", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if results == nil {
		results = []agentMCPBindingResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

// CreateAgentBinding binds an MCP server to an agent with optional credential and configuration.
// POST /api/v1/agents/{agentId}/mcp-bindings
func (h *IntegrationHandler) CreateAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	if err := agentExists(r.Context(), h.db, agentID, workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent existence", "error", err, "agent_id", agentID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req createAgentBindingRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if req.MCPServerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_server_id is required"})
		return
	}
	if req.MCPServerScope != "workspace" && req.MCPServerScope != "crew" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_server_scope must be 'workspace' or 'crew'"})
		return
	}

	// Verify MCP server exists
	switch req.MCPServerScope {
	case "workspace":
		var wsID string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM workspace_mcp_servers WHERE id = ?",
			req.MCPServerID).Scan(&wsID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Workspace integration not found"})
			return
		}
		if wsID != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Integration belongs to a different workspace"})
			return
		}
	case "crew":
		var crewWS string
		if err := h.db.QueryRowContext(r.Context(), `
			SELECT c.workspace_id FROM crew_mcp_servers cs
			JOIN crews c ON c.id = cs.crew_id
			WHERE cs.id = ?`, req.MCPServerID).Scan(&crewWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Crew integration not found"})
			return
		}
		if crewWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Integration belongs to a different workspace"})
			return
		}
	}

	// Verify credential if provided
	if req.CredentialID != nil && *req.CredentialID != "" {
		var credWS string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM credentials WHERE id = ? AND deleted_at IS NULL",
			*req.CredentialID).Scan(&credWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential not found"})
			return
		}
		if credWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential belongs to a different workspace"})
			return
		}
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}

	credType := "bearer"
	if req.CredType != nil {
		credType = *req.CredType
	}
	if credType != "bearer" && credType != "api_key" && credType != "basic" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cred_type must be 'bearer', 'api_key', or 'basic'"})
		return
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope,
			credential_id, cred_type, cred_header, env_var_name, enabled, config_override_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agentID, req.MCPServerID, req.MCPServerScope,
		req.CredentialID, credType, req.CredHeader, req.EnvVarName, enabled, req.ConfigOverride, now)
	if err != nil {
		h.logger.Error("create agent binding", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Agent already has a binding for this integration"})
		return
	}

	writeJSON(w, http.StatusCreated, agentMCPBindingResponse{
		ID: id, AgentID: agentID, MCPServerID: req.MCPServerID,
		MCPServerScope: req.MCPServerScope, CredentialID: req.CredentialID,
		CredType: &credType, CredHeader: req.CredHeader,
		Enabled: enabled == 1, ConfigOverride: req.ConfigOverride, CreatedAt: now,
	})
}

// UpdateAgentBinding modifies an existing agent MCP server binding.
// PATCH /api/v1/agents/{agentId}/mcp-bindings/{bindingId}
func (h *IntegrationHandler) UpdateAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	id := r.PathValue("integrationId")

	var req updateAgentBindingRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Verify binding exists and agent belongs to workspace
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT b.id FROM agent_mcp_bindings b
		JOIN agents a ON a.id = b.agent_id
		WHERE b.id = ? AND b.agent_id = ? AND a.workspace_id = ?`,
		id, agentID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent binding not found"})
		return
	}

	sets := []string{}
	args := []any{}
	if req.CredentialID != nil {
		if *req.CredentialID != "" {
			// Verify credential
			var credWS string
			if err := h.db.QueryRowContext(r.Context(),
				"SELECT workspace_id FROM credentials WHERE id = ? AND deleted_at IS NULL",
				*req.CredentialID).Scan(&credWS); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential not found"})
				return
			}
			if credWS != workspaceID {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential belongs to a different workspace"})
				return
			}
		}
		sets = append(sets, "credential_id = ?")
		args = append(args, req.CredentialID)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}
	if req.CredType != nil {
		if *req.CredType != "bearer" && *req.CredType != "api_key" && *req.CredType != "basic" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cred_type must be 'bearer', 'api_key', or 'basic'"})
			return
		}
		sets = append(sets, "cred_type = ?")
		args = append(args, *req.CredType)
	}
	if req.CredHeader != nil {
		sets = append(sets, "cred_header = ?")
		args = append(args, *req.CredHeader)
	}
	if req.EnvVarName != nil {
		if *req.EnvVarName == "" {
			sets = append(sets, "env_var_name = NULL")
		} else {
			sets = append(sets, "env_var_name = ?")
			args = append(args, *req.EnvVarName)
		}
	}
	if req.ConfigOverride != nil {
		sets = append(sets, "config_override_json = ?")
		args = append(args, *req.ConfigOverride)
	}

	if len(sets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	args = append(args, id)
	query := "UPDATE agent_mcp_bindings SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update agent binding", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// DeleteAgentBinding removes an MCP server binding from an agent.
// DELETE /api/v1/agents/{agentId}/mcp-bindings/{bindingId}
func (h *IntegrationHandler) DeleteAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	id := r.PathValue("integrationId")

	result, err := h.db.ExecContext(r.Context(), `
		DELETE FROM agent_mcp_bindings WHERE id = ? AND agent_id = ? AND agent_id IN
		(SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL)`, id, agentID, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent binding not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
