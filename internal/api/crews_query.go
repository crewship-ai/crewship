package api

// Crew read paths + Delete — list, get, soft-delete. Extracted from
// crews.go.

import (
	"database/sql"
	"fmt"
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
	// #1255 cites ProjectHandler.List as the precedent to copy. It is not
	// one: that query has no LIMIT/OFFSET. Without a page there is nothing
	// to bound the point lookups, so amortising one grouped aggregate over
	// the whole result is the better trade there and the worse trade here.
	// Pagination is what inverts it — re-check for a LIMIT before treating
	// any other handler as a precedent for this one.
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
		// The list is where an operator scans a fleet for "which of these is
		// actually fenced", so it carries the effective egress state too — a
		// detail-page-only answer would need one request per crew to ask.
		h.annotateEgressEnforcement(&c)
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

	h.annotateEgressEnforcement(&c)

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

	// Verify the crew exists and belongs to the workspace, and take its slug
	// from the SAME row in the SAME read.
	//
	// The slug is how the container runtime names everything belonging to this
	// crew, so the teardown below cannot find any of it once the row is
	// soft-deleted. It used to be a second query whose failure only logged,
	// which meant a transient DB error produced: crew deleted, {"success":true},
	// and a Postgres container plus its volume on disk forever with no caller
	// left that could name them — while the operator had just answered a prompt
	// saying those volumes were being deleted. One read, and no slug means no
	// delete.
	crewSlug, found, err := loadCrewForDelete(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "get crew for delete", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if crewSlug == "" {
		// A crew row with no slug cannot have its sidecars torn down, and
		// deleting it would strand them permanently. Refuse the whole delete
		// rather than half-honour it.
		replyInternalError(w, h.logger, "get crew for delete",
			fmt.Errorf("crew %s has no slug; refusing to delete it because its sidecar containers "+
				"and volumes are named by slug and could not be removed", crewID))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// The crew's agents go first, and they go the way `agent delete` sends
	// them: a deleted_at tombstone, not a DELETE (#1712).
	//
	// Why they have to go at all: every slug-uniqueness check in this package
	// is scoped to `deleted_at IS NULL`, so soft-deleting the crew below frees
	// the CREW slug — and an agent left behind keeps holding its own. The crew
	// it belonged to no longer exists, nothing can reach it, and re-applying
	// the manifest that created it answers `409 Agent slug already taken in
	// this workspace` until someone runs `crewship agent delete` by hand.
	//
	// Soft rather than hard, matching the crew's own convention: the row is
	// referenced by missions, journal entries and chats that outlive it, and
	// agents_create.go already knows how to rename a soft-deleted agent out of
	// the way (`slug || '_deleted_' || id`) when the slug is claimed again. A
	// third deletion convention here would be a third thing to keep in step.
	//
	// FIRST in the cascade, and a hard failure rather than a logged warning:
	// everything below this line destroys rows, so a failure here leaves the
	// crew wholly intact and the operator can retry. Half-deleting a crew and
	// reporting {"success":true} is what makes the slug namespace drift in the
	// first place.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET deleted_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
		now, crewID); err != nil {
		replyInternalError(w, h.logger, "cascade soft-delete crew agents", err)
		return
	}

	// Cascade: hard-delete orphan-prone children before soft-deleting the crew.
	// Missions carry a UNIQUE(workspace_id, identifier) constraint (#1733 — it
	// used to be global, which was a cross-tenant bug), and issue_counters is
	// keyed by crew_id, so a replacement crew in THIS workspace starts numbering
	// at 1 again. Leaving the old crew's issues behind would make the new crew's
	// first ENG-1 collide with a row belonging to a crew the user already
	// deleted. Other workspaces were never affected and are not now.
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
	// Links to or from this crew mean nothing once it is gone — no dispatch,
	// message or mission can cross them again. Left behind they outnumber the
	// live ones within a few reseeds and bury the real graph.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM crew_connections WHERE from_crew_id = ? OR to_crew_id = ?", crewID, crewID); err != nil {
		h.logger.Warn("cascade delete crew_connections", "crew_id", crewID, "error", err)
	}

	_, err = h.db.ExecContext(r.Context(),
		"UPDATE crews SET deleted_at = ? WHERE id = ?",
		now, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "soft delete crew", err)
		return
	}

	teardown := h.removeCrewSidecars(r.Context(), crewID, crewSlug)

	auditFromRequest(r, h.db, "crew.delete", "CREW", crewID, nil)

	// The teardown outcome travels with the response. The operator answered a
	// confirmation that named the volumes this would delete; if it did not
	// delete them — because another crew shares the slug-keyed namespace, or the
	// daemon refused — they have to hear it from the command they ran, not from
	// a server log they will never read.
	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"sidecar_teardown": teardown,
	})

	h.broadcastCrewEvent("crew.deleted", workspaceID, map[string]string{"id": crewID})
}
