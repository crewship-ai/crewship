package api

import (
	"database/sql"
	"net/http"
)

// ==========================================
// Cascade Resolution
// ==========================================

// ResolvedIntegration is the effective MCP server config for a specific agent.
type ResolvedIntegration struct {
	ServerID     string  `json:"server_id"`
	Scope        string  `json:"scope"` // "workspace" or "crew"
	Name         string  `json:"name"`
	DisplayName  string  `json:"display_name"`
	Transport    string  `json:"transport"`
	Endpoint     *string `json:"endpoint"`
	Command      *string `json:"command"`
	ArgsJSON     *string `json:"args_json"`
	EnvJSON      *string `json:"env_json"`
	ConfigJSON   *string `json:"config_json"`
	Icon         *string `json:"icon"`
	Enabled      bool    `json:"enabled"`
	CredentialID *string `json:"credential_id"`
	CredName     *string `json:"credential_name"`
}

func (h *IntegrationHandler) ResolveAgentIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")

	// Get agent's crew_id from the agents table
	var crewID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	// Step 1: Workspace MCP servers
	wsServers := make(map[string]*ResolvedIntegration)
	if wsRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, display_name, transport, endpoint, command,
			args_json, env_json, config_json, icon, enabled
		FROM workspace_mcp_servers
		WHERE workspace_id = ? AND enabled = 1 AND deleted_at IS NULL`, workspaceID); err == nil {
		for wsRows.Next() {
			var s ResolvedIntegration
			var enabled int
			if err := wsRows.Scan(&s.ServerID, &s.Name, &s.DisplayName, &s.Transport,
				&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
				&s.Icon, &enabled); err != nil {
				continue
			}
			s.Scope = "workspace"
			s.Enabled = enabled == 1
			wsServers[s.Name] = &s
		}
		if err := wsRows.Err(); err != nil {
			h.logger.Error("iterate workspace MCP servers", "error", err)
		}
		wsRows.Close()
	}

	// Step 2: Crew MCP servers (override workspace by name)
	merged := make(map[string]*ResolvedIntegration)
	for k, v := range wsServers {
		merged[k] = v
	}

	if crewID.Valid {
		if crewRows, err := h.db.QueryContext(r.Context(), `
			SELECT id, workspace_mcp_server_id, name, display_name, transport,
				endpoint, command, args_json, env_json, config_json, icon, enabled
			FROM crew_mcp_servers
			WHERE crew_id = ? AND enabled = 1 AND deleted_at IS NULL`, crewID.String); err == nil {
			for crewRows.Next() {
				var s ResolvedIntegration
				var wsServerID sql.NullString
				var enabled int
				if err := crewRows.Scan(&s.ServerID, &wsServerID, &s.Name, &s.DisplayName, &s.Transport,
					&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
					&s.Icon, &enabled); err != nil {
					continue
				}
				s.Scope = "crew"
				s.Enabled = enabled == 1
				merged[s.Name] = &s
			}
			if err := crewRows.Err(); err != nil {
				h.logger.Error("iterate crew MCP servers", "error", err)
			}
			crewRows.Close()
		}
	}

	// Step 3: Apply agent bindings (opt-out and credential assignment)
	type bindingInfo struct {
		credentialID *string
		credName     *string
		enabled      bool
		configJSON   *string
	}
	bindings := make(map[string]*bindingInfo)
	if bindingRows, err := h.db.QueryContext(r.Context(), `
		SELECT b.mcp_server_id, b.mcp_server_scope, b.credential_id, b.enabled, b.config_override_json,
			c.name AS cred_name
		FROM agent_mcp_bindings b
		LEFT JOIN credentials c ON b.credential_id = c.id
		WHERE b.agent_id = ?`, agentID); err == nil {
		for bindingRows.Next() {
			var serverID, scope string
			var credID, credName, configJSON *string
			var enabled int
			if err := bindingRows.Scan(&serverID, &scope, &credID, &enabled, &configJSON, &credName); err != nil {
				continue
			}
			bindings[serverID] = &bindingInfo{
				credentialID: credID, credName: credName,
				enabled: enabled == 1, configJSON: configJSON,
			}
		}
		if err := bindingRows.Err(); err != nil {
			h.logger.Error("iterate agent MCP bindings", "error", err)
		}
		bindingRows.Close()
	}
	{

		// Apply bindings to merged servers
		for _, s := range merged {
			if b, ok := bindings[s.ServerID]; ok {
				if !b.enabled {
					s.Enabled = false
				}
				s.CredentialID = b.credentialID
				s.CredName = b.credName
				if b.configJSON != nil {
					s.ConfigJSON = b.configJSON
				}
			}
		}
	}

	// Check which servers have ANY bindings (for opt-in filtering), scoped to this workspace.
	serversWithBindings := make(map[string]bool)
	if bcRows, err := h.db.QueryContext(r.Context(), `
		SELECT b.mcp_server_id FROM agent_mcp_bindings b
		JOIN agents a ON a.id = b.agent_id AND a.workspace_id = ?
		GROUP BY b.mcp_server_id HAVING COUNT(*) > 0`, workspaceID); err == nil {
		for bcRows.Next() {
			var sid string
			if bcRows.Scan(&sid) == nil {
				serversWithBindings[sid] = true
			}
		}
		if err := bcRows.Err(); err != nil {
			h.logger.Error("iterate servers with bindings", "error", err)
		}
		bcRows.Close()
	}

	// Build result (only enabled, respecting opt-in bindings)
	var result []ResolvedIntegration
	for _, s := range merged {
		if !s.Enabled {
			continue
		}
		_, hasBind := bindings[s.ServerID]
		if !hasBind && serversWithBindings[s.ServerID] {
			// Server has bindings for other agents but not this one → skip
			continue
		}
		result = append(result, *s)
	}
	if result == nil {
		result = []ResolvedIntegration{}
	}
	writeJSON(w, http.StatusOK, result)
}
