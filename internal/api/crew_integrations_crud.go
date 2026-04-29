package api

// Crew integration CRUD: create / update / delete handlers + the
// createCrewIntegrationRequest payload type. Extracted from
// crew_integrations.go for readability.

import (
	"database/sql"
	"net/http"
	"time"
)

type createCrewIntegrationRequest struct {
	WorkspaceMCPServerID *string `json:"workspace_mcp_server_id"`
	Name                 string  `json:"name"`
	DisplayName          string  `json:"display_name"`
	Transport            string  `json:"transport"`
	Endpoint             *string `json:"endpoint"`
	Command              *string `json:"command"`
	ArgsJSON             *string `json:"args_json"`
	EnvJSON              *string `json:"env_json"`
	ConfigJSON           *string `json:"config_json"`
	Icon                 *string `json:"icon"`
}

// ==========================================
// All crew integrations (cross-crew view for Integrations page)
// ==========================================

// ListAllCrewIntegrations returns all MCP server integrations across all crews in the workspace.
// GET /api/v1/integrations/crews — used by the cross-crew integrations overview page.

func (h *IntegrationHandler) CreateCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	// Verify crew
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("crew exists check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}

	var req createCrewIntegrationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if req.Transport == "" {
		req.Transport = "streamable-http"
	}
	if req.Transport != "streamable-http" && req.Transport != "stdio" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
		return
	}
	if req.Transport == "streamable-http" && (req.Endpoint == nil || *req.Endpoint == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required for streamable-http transport"})
		return
	}
	if req.Transport == "stdio" && (req.Command == nil || *req.Command == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required for stdio transport"})
		return
	}

	// If linking to workspace server, verify it exists and belongs to same workspace
	if req.WorkspaceMCPServerID != nil && *req.WorkspaceMCPServerID != "" {
		var wsServerWS string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM workspace_mcp_servers WHERE id = ?",
			*req.WorkspaceMCPServerID).Scan(&wsServerWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Referenced workspace integration not found"})
			return
		}
		if wsServerWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Workspace integration belongs to a different workspace"})
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO crew_mcp_servers (id, crew_id, workspace_mcp_server_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, crewID, req.WorkspaceMCPServerID, req.Name, req.DisplayName, req.Transport,
		req.Endpoint, req.Command, req.ArgsJSON, req.EnvJSON, req.ConfigJSON, req.Icon, now, now)
	if err != nil {
		h.logger.Error("create crew integration", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Integration with this name already exists in this crew"})
		return
	}

	h.broadcastEvent("integration.created", workspaceID, map[string]string{
		"id": id, "name": req.Name, "scope": "crew", "crew_id": crewID,
	})

	writeJSON(w, http.StatusCreated, crewMCPServerResponse{
		ID: id, CrewID: crewID, WorkspaceMCPServerID: req.WorkspaceMCPServerID,
		Name: req.Name, DisplayName: req.DisplayName, Transport: req.Transport,
		Endpoint: req.Endpoint, Command: req.Command,
		ArgsJSON: req.ArgsJSON, EnvJSON: req.EnvJSON, ConfigJSON: req.ConfigJSON,
		Icon: req.Icon, Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
}

// UpdateCrewIntegration modifies an existing MCP server integration on a crew.
// PATCH /api/v1/crews/{crewId}/integrations/{integrationId}

func (h *IntegrationHandler) UpdateCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	id := r.PathValue("integrationId")

	var req updateIntegrationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Verify crew + server exist and are not soft-deleted
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT cs.id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id
		WHERE cs.id = ? AND cs.crew_id = ? AND c.workspace_id = ?
			AND cs.deleted_at IS NULL AND c.deleted_at IS NULL`,
		id, crewID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew integration not found"})
		return
	}

	u := newUpdate()
	if req.DisplayName != nil {
		u.Set("display_name", *req.DisplayName)
	}
	if req.Transport != nil {
		if *req.Transport != "streamable-http" && *req.Transport != "stdio" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
			return
		}
		u.Set("transport", *req.Transport)
	}
	if req.Endpoint != nil {
		u.Set("endpoint", *req.Endpoint)
	}
	if req.Command != nil {
		u.Set("command", *req.Command)
	}
	if req.ArgsJSON != nil {
		u.Set("args_json", *req.ArgsJSON)
	}
	if req.EnvJSON != nil {
		u.Set("env_json", *req.EnvJSON)
	}
	if req.ConfigJSON != nil {
		u.Set("config_json", *req.ConfigJSON)
	}
	if req.Icon != nil {
		u.Set("icon", *req.Icon)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		u.Set("enabled", enabled)
	}

	// Validate transport/field combination against merged final state
	if req.Transport != nil {
		var existingEndpoint, existingCommand sql.NullString
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT endpoint, command FROM crew_mcp_servers WHERE id = ?", id).
			Scan(&existingEndpoint, &existingCommand); err != nil {
			h.logger.Error("load existing crew integration", "id", id, "error", err)
		}

		finalEndpoint := existingEndpoint.String
		if req.Endpoint != nil {
			finalEndpoint = *req.Endpoint
		}
		finalCommand := existingCommand.String
		if req.Command != nil {
			finalCommand = *req.Command
		}

		if *req.Transport == "streamable-http" && finalEndpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required for streamable-http transport"})
			return
		}
		if *req.Transport == "stdio" && finalCommand == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required for stdio transport"})
			return
		}
	}

	query, args := u.Build("crew_mcp_servers", "id = ?", id)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update crew integration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.updated", workspaceID, map[string]string{
		"id": id, "scope": "crew", "crew_id": crewID,
	})

	// Return updated
	var s crewMCPServerResponse
	var enabled int
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id, crew_id, workspace_mcp_server_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = crew_mcp_servers.id AND mcp_server_scope = 'crew')
		FROM crew_mcp_servers WHERE id = ?`, id).Scan(
		&s.ID, &s.CrewID, &s.WorkspaceMCPServerID, &s.Name, &s.DisplayName, &s.Transport,
		&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
		&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt, &s.AgentBindCount); err != nil {
		h.logger.Error("fetch updated crew integration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	s.Enabled = enabled == 1
	writeJSON(w, http.StatusOK, s)
}

// DeleteCrewIntegration removes an MCP server integration from a crew and its agent bindings.
// DELETE /api/v1/crews/{crewId}/integrations/{integrationId}

func (h *IntegrationHandler) DeleteCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	id := r.PathValue("integrationId")

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Collect credential IDs from bindings — cascade-delete OAuth credentials
	// that were created specifically for this integration (auto-connect flow).
	var credIDs []string
	rows, err := tx.QueryContext(r.Context(),
		`SELECT DISTINCT ab.credential_id FROM agent_mcp_bindings ab
		 JOIN credentials c ON c.id = ab.credential_id
		 WHERE ab.mcp_server_id = ? AND ab.mcp_server_scope = 'crew'
		   AND c.type = 'OAUTH2' AND c.name LIKE '%oauth%'`,
		id)
	if err == nil {
		for rows.Next() {
			var cid string
			if rows.Scan(&cid) == nil && cid != "" {
				credIDs = append(credIDs, cid)
			}
		}
		if err := rows.Err(); err != nil {
			h.logger.Error("iterate credential IDs for deletion", "error", err)
		}
		rows.Close()
	}

	// Delete agent bindings for this crew server
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'crew'", id); err != nil {
		tx.Rollback()
		h.logger.Error("delete agent bindings for crew server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Cascade-delete OAuth credentials only if no other bindings reference them
	for _, cid := range credIDs {
		var remaining int
		if err := tx.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM agent_mcp_bindings WHERE credential_id = ?", cid).Scan(&remaining); err != nil {
			h.logger.Warn("check credential bindings", "credential_id", cid, "error", err)
			continue
		}
		if remaining > 0 {
			continue // still referenced elsewhere
		}
		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM credentials WHERE id = ? AND workspace_id = ?", cid, workspaceID); err != nil {
			h.logger.Warn("cascade delete OAuth credential", "credential_id", cid, "error", err)
		}
	}

	result, err := tx.ExecContext(r.Context(), `
		DELETE FROM crew_mcp_servers WHERE id = ? AND crew_id = ? AND crew_id IN
		(SELECT id FROM crews WHERE workspace_id = ?)`, id, crewID, workspaceID)
	if err != nil {
		tx.Rollback()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		tx.Rollback()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew integration not found"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.deleted", workspaceID, map[string]string{
		"id": id, "scope": "crew", "crew_id": crewID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// JSON blob → integration table migration
// ---------------------------------------------------------------------------

// parsedMCPServer holds the parsed fields for a single MCP server entry
// extracted from an mcp_config_json blob.
