package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
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
	// DefaultAccess is the server's stored audience — "all" (every agent in
	// scope) or "bound-only" (agents with an explicit binding). It is echoed
	// so a caller can tell WHY a server resolved without a second lookup.
	DefaultAccess string `json:"default_access"`
}

// accessAll is the one default_access value that lets an agent with no
// binding use a server. Anything else — including a value some future writer
// invents — resolves closed, so a typo cannot silently widen an audience.
const accessAll = "all"

// accessBoundOnly restricts a server to agents holding an explicit binding.
const accessBoundOnly = "bound-only"

// openToUnboundAgents reports whether a server whose default_access column
// holds v is available to agents that have no binding for it.
func openToUnboundAgents(v string) bool { return v == accessAll }

// normalizeDefaultAccess validates an incoming default_access value. The
// vocabulary is closed and enforced here rather than by a CHECK constraint —
// SQLite cannot alter one in place, and the integration handlers are the only
// writers. Returns the canonical value and whether it was recognised; callers
// treat the empty string as "not supplied", never as a value.
func normalizeDefaultAccess(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case accessAll:
		return accessAll, true
	case accessBoundOnly:
		return accessBoundOnly, true
	default:
		return "", false
	}
}

// errDefaultAccessVocabulary is the 400 body for a default_access the server
// does not recognise. Naming both values matters: resolution fails closed, so
// silently accepting "All" would revoke the server from every unbound agent.
const errDefaultAccessVocabulary = "default_access must be 'all' or 'bound-only'"

// ResolveAgentIntegrations returns the effective MCP server configuration for an agent
// by cascading workspace-level and crew-level integrations.
// GET /api/v1/agents/{agentId}/resolved-integrations
func (h *IntegrationHandler) ResolveAgentIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")

	// Get agent's crew_id from the agents table
	var crewID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Agent not found")
		} else {
			replyInternalError(w, h.logger, "lookup agent for integration resolution", err)
		}
		return
	}

	// Step 1: Workspace MCP servers
	wsServers := make(map[string]*ResolvedIntegration)
	wsRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, display_name, transport, endpoint, command,
			args_json, env_json, config_json, icon, enabled, default_access
		FROM workspace_mcp_servers
		WHERE workspace_id = ? AND enabled = 1 AND deleted_at IS NULL`, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "query workspace MCP servers", err)
		return
	}
	for wsRows.Next() {
		var s ResolvedIntegration
		var enabled int
		if err := wsRows.Scan(&s.ServerID, &s.Name, &s.DisplayName, &s.Transport,
			&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
			&s.Icon, &enabled, &s.DefaultAccess); err != nil {
			h.logger.Error("scan workspace MCP server", "error", err)
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

	// Step 2: Crew MCP servers (override workspace by name)
	merged := make(map[string]*ResolvedIntegration)
	for k, v := range wsServers {
		merged[k] = v
	}

	if crewID.Valid {
		crewRows, err := h.db.QueryContext(r.Context(), `
			SELECT id, workspace_mcp_server_id, name, display_name, transport,
				endpoint, command, args_json, env_json, config_json, icon, enabled,
				default_access
			FROM crew_mcp_servers
			WHERE crew_id = ? AND enabled = 1 AND deleted_at IS NULL`, crewID.String)
		if err != nil {
			replyInternalError(w, h.logger, "query crew MCP servers", err)
			return
		}
		for crewRows.Next() {
			var s ResolvedIntegration
			var wsServerID sql.NullString
			var enabled int
			if err := crewRows.Scan(&s.ServerID, &wsServerID, &s.Name, &s.DisplayName, &s.Transport,
				&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
				&s.Icon, &enabled, &s.DefaultAccess); err != nil {
				h.logger.Error("scan crew MCP server", "error", err)
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

	// Step 3: Apply agent bindings (opt-out and credential assignment)
	type bindingInfo struct {
		credentialID *string
		credName     *string
		enabled      bool
		configJSON   *string
	}
	bindings := make(map[string]*bindingInfo)
	bindingRows, err := h.db.QueryContext(r.Context(), `
		SELECT b.mcp_server_id, b.mcp_server_scope, b.credential_id, b.enabled, b.config_override_json,
			c.name AS cred_name
		FROM agent_mcp_bindings b
		LEFT JOIN credentials c ON b.credential_id = c.id
		WHERE b.agent_id = ?`, agentID)
	if err != nil {
		replyInternalError(w, h.logger, "query agent MCP bindings", err)
		return
	}
	for bindingRows.Next() {
		var serverID, scope string
		var credID, credName, configJSON *string
		var enabled int
		if err := bindingRows.Scan(&serverID, &scope, &credID, &enabled, &configJSON, &credName); err != nil {
			h.logger.Error("scan agent MCP binding", "error", err)
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

	// Build result: enabled servers this agent is entitled to.
	//
	// Entitlement is the server's own default_access column (#2072). It used
	// to be inferred from whether ANY agent in the workspace held a binding,
	// which made the first binding a workspace-wide revocation: the state
	// "available to everyone" was never stored, so it disappeared as soon as
	// the count it was derived from stopped being zero.
	var result []ResolvedIntegration
	for _, s := range merged {
		if !s.Enabled {
			continue
		}
		_, hasBind := bindings[s.ServerID]
		if !hasBind && !openToUnboundAgents(s.DefaultAccess) {
			// bound-only, and this agent has no binding → not for them.
			continue
		}
		result = append(result, *s)
	}
	if result == nil {
		result = []ResolvedIntegration{}
	}
	writeJSON(w, http.StatusOK, result)
}
