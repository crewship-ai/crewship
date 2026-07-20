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

	// The two per-row COUNT subqueries below look like the classic N+1 to
	// rewrite as a grouped LEFT JOIN, but measurement says otherwise
	// (#1255 item 2). Each subquery is a point lookup on an existing index
	// (idx_agent_crew, idx_crew_member_crew), so the total work is bounded
	// by the page size — at most 2×LIMIT probes, independent of how many
	// agents or members the workspace holds. Any grouped-aggregate rewrite
	// has to touch every agent/crew_member row in the workspace before the
	// LIMIT applies, so it is the version that degrades with workspace
	// size. Benchmarked on a seeded SQLite fixture, 500-row page:
	//
	//   500 crews / 20k agents / 10k members: subqueries 5.9ms, join 29.2ms
	//   3000 crews / 24k agents / 12k members: subqueries 2.4ms, join 51.0ms
	//
	// Do not "optimise" this into a join without re-running that
	// measurement. TestCrewListCountsGoldenFixture locks the observable
	// contract if you do.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains, c.allow_private_endpoints,
			c.mcp_config_json, c.escalation_config,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.services_json, c.cached_image, c.config_hash,
			c.max_ephemeral_agents,
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
		replyInternalError(w, h.logger, "list crews", err)
		return
	}
	defer rows.Close()

	var result []crewResponse
	for rows.Next() {
		var c crewResponse
		if err := scanCrewRow(rows, &c, false, true); err != nil {
			replyInternalError(w, h.logger, "scan crew", err)
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (crews)", err)
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
	err := scanCrewRow(h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.workspace_id, c.name, c.slug, c.description, c.color, c.icon, c.avatar_style,
			c.container_memory_mb, c.container_cpus, c.container_ttl_hours, c.network_mode, c.allowed_domains, c.allow_private_endpoints,
			c.mcp_config_json, c.escalation_config, c.issue_prefix,
			c.runtime_image, c.devcontainer_config, c.mise_config, c.services_json, c.cached_image, c.config_hash,
			c.max_ephemeral_agents,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agents WHERE crew_id = c.id AND deleted_at IS NULL) AS agent_count,
			(SELECT COUNT(*) FROM crew_members WHERE crew_id = c.id) AS member_count
		FROM crews c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL
	`, crewID, workspaceID), &c, true, true)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		replyInternalError(w, h.logger, "get crew", err)
		return
	}

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
		replyInternalError(w, h.logger, "get crew for delete", err)
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
		replyInternalError(w, h.logger, "soft delete crew", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	h.broadcastCrewEvent("crew.deleted", workspaceID, map[string]string{"id": crewID})
}
