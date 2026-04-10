package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// resolveCrewMembers fetches peer agents within the same crew and enriches
// LEAD/COORDINATOR agents with MCP integration info.
func (h *InternalHandler) resolveCrewMembers(r *http.Request, data *agentConfigData, agentID string) ([]crewMemberEntry, error) {
	crewMembers := []crewMemberEntry{}
	if !data.crewID.Valid {
		return crewMembers, nil
	}

	memberRows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.name, a.slug, COALESCE(a.role_title, ''), COALESCE(a.description, ''), a.status,
		       COALESCE((SELECT c.id FROM chats c WHERE c.agent_id = a.id AND c.status = 'ACTIVE' ORDER BY c.created_at DESC LIMIT 1), '')
		FROM agents a
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		ORDER BY a.name
	`, data.crewID.String, agentID)
	if err != nil {
		return crewMembers, err
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var m crewMemberEntry
		if err := memberRows.Scan(&m.ID, &m.Name, &m.Slug, &m.RoleTitle, &m.Description, &m.Status, &m.ChatID); err != nil {
			h.logger.Error("scan crew member", "error", err)
			continue
		}
		crewMembers = append(crewMembers, m)
	}
	if err := memberRows.Err(); err != nil {
		h.logger.Error("rows iteration (crew members)", "error", err)
	}

	// Enrich crew members with MCP integration info (single batch query)
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
	if (roleStr == "LEAD" || roleStr == "COORDINATOR") && len(crewMembers) > 0 {
		memberIdx := make(map[string]int, len(crewMembers))
		placeholders := make([]string, len(crewMembers))
		args := make([]interface{}, len(crewMembers))
		for i, m := range crewMembers {
			memberIdx[m.ID] = i
			placeholders[i] = "?"
			args[i] = m.ID
		}
		if igRows, err := h.db.QueryContext(r.Context(), `
			SELECT b.agent_id,
				COALESCE(CASE b.mcp_server_scope
					WHEN 'workspace' THEN ws.display_name
					WHEN 'crew' THEN cs.display_name END, ''),
				COALESCE(CASE b.mcp_server_scope
					WHEN 'workspace' THEN ws.name
					WHEN 'crew' THEN cs.name END, '')
			FROM agent_mcp_bindings b
			LEFT JOIN workspace_mcp_servers ws ON b.mcp_server_id = ws.id AND b.mcp_server_scope = 'workspace' AND ws.deleted_at IS NULL
			LEFT JOIN crew_mcp_servers cs ON b.mcp_server_id = cs.id AND b.mcp_server_scope = 'crew' AND cs.deleted_at IS NULL
			WHERE b.agent_id IN (`+strings.Join(placeholders, ",")+`) AND b.enabled = 1`,
			args...); err == nil {
			for igRows.Next() {
				var aid, displayName, serverName string
				if igRows.Scan(&aid, &displayName, &serverName) == nil && serverName != "" {
					if idx, ok := memberIdx[aid]; ok {
						crewMembers[idx].Integrations = append(crewMembers[idx].Integrations,
							memberIntegrationEntry{Name: displayName, ServerName: serverName})
					}
				}
			}
			igRows.Close()
		}
	}

	return crewMembers, nil
}

// resolveCoordinatorCrews loads all workspace crews and their agents for COORDINATOR agents.
func (h *InternalHandler) resolveCoordinatorCrews(r *http.Request, data *agentConfigData) []crewInfoEntry {
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
	if roleStr != "COORDINATOR" {
		return nil
	}

	crewRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`,
		data.wsID)
	if err != nil {
		h.logger.Error("query crews for coordinator", "error", err)
		return nil
	}
	defer crewRows.Close()

	var allCrews []crewInfoEntry
	for crewRows.Next() {
		var ci crewInfoEntry
		if err := crewRows.Scan(&ci.ID, &ci.Name, &ci.Slug); err != nil {
			h.logger.Error("scan crew for coordinator", "error", err)
			continue
		}
		// Initialize Members as a non-nil empty slice so crews with zero
		// agents serialize as `"members": []` not `"members": null`.
		// Frontend consumers expect an array; the test
		// TestResolveCoordinatorCrews_EmptyCrew pins this contract.
		ci.Members = []crewMemberEntry{}
		agentRows, err := h.db.QueryContext(r.Context(), `
			SELECT a.id, a.name, a.slug, COALESCE(a.role_title, ''), COALESCE(a.description, ''), a.status,
			       COALESCE((SELECT c.id FROM chats c WHERE c.agent_id = a.id AND c.status = 'ACTIVE' ORDER BY c.created_at DESC LIMIT 1), '')
			FROM agents a
			WHERE a.crew_id = ? AND a.deleted_at IS NULL
			ORDER BY a.name`, ci.ID)
		if err != nil {
			h.logger.Error("query agents for coordinator crew", "error", err, "crew_id", ci.ID)
		} else {
			for agentRows.Next() {
				var m crewMemberEntry
				if err := agentRows.Scan(&m.ID, &m.Name, &m.Slug, &m.RoleTitle, &m.Description, &m.Status, &m.ChatID); err != nil {
					h.logger.Error("scan agent for coordinator", "error", err)
					continue
				}
				ci.Members = append(ci.Members, m)
			}
			agentRows.Close()
		}
		allCrews = append(allCrews, ci)
	}
	return allCrews
}

// resolveNetworkPolicy determines the network mode and allowed domains for the agent's crew.
func (h *InternalHandler) resolveNetworkPolicy(data *agentConfigData) (string, []string) {
	crewIDStr := ""
	if data.crewID.Valid {
		crewIDStr = data.crewID.String
	}

	networkMode := "free"
	if data.crewNetworkMode.Valid && data.crewNetworkMode.String != "" {
		mode := data.crewNetworkMode.String
		if mode == "free" || mode == "restricted" {
			networkMode = mode
		} else {
			// Unknown mode in DB -- fail closed to prevent silent egress
			h.logger.Error("unknown network_mode in DB, defaulting to restricted", "mode", mode, "crew_id", crewIDStr)
			networkMode = "restricted"
		}
	}
	allowedDomains := []string{}
	if data.crewAllowedDomains.Valid && data.crewAllowedDomains.String != "" {
		if err := json.Unmarshal([]byte(data.crewAllowedDomains.String), &allowedDomains); err != nil {
			h.logger.Error("malformed allowed_domains JSON in DB, defaulting to empty", "error", err, "crew_id", crewIDStr)
			allowedDomains = []string{}
		}
	}
	return networkMode, allowedDomains
}

// resolveContainerResources extracts container resource limits from crew data.
func (h *InternalHandler) resolveContainerResources(data *agentConfigData) (int, float64, int) {
	memoryMB := 4096
	if data.crewMemoryMB.Valid {
		memoryMB = int(data.crewMemoryMB.Int64)
	}
	cpus := 2.0
	if data.crewCPUs.Valid {
		cpus = data.crewCPUs.Float64
	}
	ttlHours := 0
	if data.crewTTLHours.Valid {
		ttlHours = int(data.crewTTLHours.Int64)
	}
	return memoryMB, cpus, ttlHours
}
