package api

import (
	"database/sql"
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
	// COORDINATOR branch is deprecated (see [BuildCoordinatorContext]); retained for backward compat.
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
//
// Historical note: this used to be 1 + N + N*M queries (one per crew, then a
// scalar chat subquery per agent row) — a classic N+1. It's now a single query
// that LEFT JOINs crews → agents, preserving the "empty crew still listed"
// semantic, and the remaining chat lookup is an O(log n) index hit on
// idx_chat_agent_status_created(agent_id, status, created_at DESC). Grouping
// by crew happens in Go since rows come in deterministic order.
//
// Ported from PR #132 after the feat/code-quality file-splits refactor moved
// this function out of agent_config_resolver.go.
//
// Deprecated: COORDINATOR role is deprecated (2026-04-16). See
// [BuildCoordinatorContext] in internal/orchestrator/lead.go. Retained for
// backward compat with existing COORDINATOR agents.
func (h *InternalHandler) resolveCoordinatorCrews(r *http.Request, data *agentConfigData) []crewInfoEntry {
	roleStr := "AGENT"
	if data.agentRole.Valid && data.agentRole.String != "" {
		roleStr = data.agentRole.String
	}
	if roleStr != "COORDINATOR" {
		return nil
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.name, c.slug,
		       a.id, a.name, a.slug,
		       COALESCE(a.role_title, ''), COALESCE(a.description, ''),
		       COALESCE(a.status, ''),
		       COALESCE((
		           SELECT ch.id FROM chats ch
		           WHERE ch.agent_id = a.id AND ch.status = 'ACTIVE'
		           ORDER BY ch.created_at DESC LIMIT 1
		       ), '')
		FROM crews c
		LEFT JOIN agents a ON a.crew_id = c.id AND a.deleted_at IS NULL
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		-- c.id is the tie-breaker: the schema only has UNIQUE(workspace_id, slug),
		-- so two crews can share a name. Without this, same-named crews would
		-- interleave their agent rows and the streaming grouping below would
		-- produce duplicate crew entries.
		ORDER BY c.name, c.id, a.name`, data.wsID)
	if err != nil {
		h.logger.Error("query crews+agents for coordinator", "error", err)
		return nil
	}
	defer rows.Close()

	// Rows arrive ordered by crew name, so we can stream them into buckets
	// keyed on the current crew ID without building a map.
	var allCrews []crewInfoEntry
	var currentCrewID string
	for rows.Next() {
		var crewID, crewName, crewSlug string
		var agentID, agentName, agentSlug sql.NullString
		var roleTitle, description, status, chatID string
		if err := rows.Scan(
			&crewID, &crewName, &crewSlug,
			&agentID, &agentName, &agentSlug,
			&roleTitle, &description, &status, &chatID,
		); err != nil {
			h.logger.Error("scan crew+agent row", "error", err)
			continue
		}

		if crewID != currentCrewID {
			// Members is deliberately initialized as a non-nil empty slice so
			// crews with zero members serialize as `"members": []` in JSON
			// (not `"members": null`). The frontend consumers consistently
			// expect an array; matching that contract here avoids a null
			// check on every caller.
			allCrews = append(allCrews, crewInfoEntry{
				ID:      crewID,
				Name:    crewName,
				Slug:    crewSlug,
				Members: []crewMemberEntry{},
			})
			currentCrewID = crewID
		}

		// LEFT JOIN: crews with no agents produce a single row with NULL
		// agent_id. Skip that row — the crew entry is already recorded.
		if !agentID.Valid {
			continue
		}

		ci := &allCrews[len(allCrews)-1]
		ci.Members = append(ci.Members, crewMemberEntry{
			ID:          agentID.String,
			Name:        agentName.String,
			Slug:        agentSlug.String,
			RoleTitle:   roleTitle,
			Description: description,
			Status:      status,
			ChatID:      chatID,
		})
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (coordinator crews)", "error", err)
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
