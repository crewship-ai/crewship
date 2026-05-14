package api

// Crew read paths + Delete — list, get, soft-delete. Extracted from
// crews.go.

import (
	"database/sql"
	"net/http"
	"time"
)

func (h *CrewHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	limit, offset := parseListPagination(r, 100, 500)

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.cached_image, c.config_hash,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		-- c.id DESC is the pagination tiebreaker: c.created_at is second-precision,
		-- so timestamp ties are realistic and would otherwise make LIMIT/OFFSET
		-- windows drop or duplicate rows between pages.
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT ? OFFSET ?
	`, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list crews", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []crewResponse
	for rows.Next() {
		var c crewResponse
		var allowedDomainsJSON *string
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
			&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
			&c.ContainerTTLHours, &c.NetworkMode, &allowedDomainsJSON,
			&c.MCPConfigJSON, &c.EscalationConfig,
			&c.RuntimeImage, &c.DevcontainerConfig, &c.MiseConfig, &c.CachedImage, &c.ConfigHash,
			&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members); err != nil {
			h.logger.Error("scan crew", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		c.AllowedDomains = parseAllowedDomains(allowedDomainsJSON)
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (crews)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []crewResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

// Create provisions a new crew in the workspace with the given name, slug, and configuration.
// POST /api/v1/crews

func (h *CrewHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	var c crewResponse
	var allowedDomainsJSON *string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains,
			c.mcp_config_json, c.escalation_config, c.issue_prefix,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.cached_image, c.config_hash,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
	`, crewID, workspaceID).Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Slug, &c.Description,
		&c.Color, &c.Icon, &c.AvatarStyle, &c.ContainerMemoryMB, &c.ContainerCPUs,
		&c.ContainerTTLHours, &c.NetworkMode, &allowedDomainsJSON,
		&c.MCPConfigJSON, &c.EscalationConfig, &c.IssuePrefix,
		&c.RuntimeImage, &c.DevcontainerConfig, &c.MiseConfig, &c.CachedImage, &c.ConfigHash,
		&c.CreatedAt, &c.UpdatedAt, &c.Count.Agents, &c.Count.Members)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	c.AllowedDomains = parseAllowedDomains(allowedDomainsJSON)
	writeJSON(w, http.StatusOK, c)
}

// Update modifies crew properties such as name, description, network policy, and escalation config.
// PATCH /api/v1/crews/{crewId}

func (h *CrewHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Verify crew exists and belongs to workspace
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("get crew for delete", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Cascade: hard-delete orphan-prone children before soft-deleting the crew.
	// Missions have a UNIQUE(identifier) constraint that is NOT workspace-scoped,
	// so leaving them behind blocks future crews from reusing identifier prefixes.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM mission_tasks WHERE mission_id IN (SELECT id FROM missions WHERE crew_id = ?)", crewID); err != nil {
		h.logger.Warn("cascade delete mission_tasks", "crew_id", crewID, "error", err)
	}
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM missions WHERE crew_id = ?", crewID); err != nil {
		h.logger.Warn("cascade delete missions", "crew_id", crewID, "error", err)
	}
	// Also remove crew members — they reference this crew
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM crew_members WHERE crew_id = ?", crewID); err != nil {
		h.logger.Warn("cascade delete crew_members", "crew_id", crewID, "error", err)
	}

	_, err = h.db.ExecContext(r.Context(),
		"UPDATE crews SET deleted_at = ? WHERE id = ?",
		now, crewID)
	if err != nil {
		h.logger.Error("soft delete crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	h.broadcastCrewEvent("crew.deleted", workspaceID, map[string]string{"id": crewID})
}
