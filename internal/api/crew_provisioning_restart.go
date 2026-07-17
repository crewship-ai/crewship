package api

// Crew runtime container restart endpoint + small helpers used by
// ProvisionStatus to report agents_pending_restart. Extracted from
// crew_provisioning.go.

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// findCrewContainer returns the live Docker container ID for the given crew,
// or "" if no container is running. Returns an error only on a real Docker
// failure — "no container found" is the empty-string success path.
//
// The docker provider names crew containers "<prefix>-team-<slug>-<crewID>"
// (crewResourceName): the prefix is instance-specific for multi-instance
// isolation ("crewship", "crewship-2", …) and the trailing crew id is the
// C1 cross-tenant fix (two workspaces may share a slug; the globally-unique
// crew id disambiguates). Matching the old hardcoded "crewship-team-<slug>"
// therefore found NOTHING on any instance-prefixed or post-C1 deployment,
// so RestartCrewAgents silently returned {restarted:0} without dropping the
// container. We match on the stable tail "-team-<slug>-<crewID>", which is
// prefix-agnostic and can't cross-match a same-slug crew in another
// workspace (the crew id is unique).
func (h *ProvisioningHandler) findCrewContainer(ctx context.Context, crewID, slug string) (string, error) {
	if h.docker == nil {
		return "", nil
	}
	suffix := "-team-" + slug + "-" + crewID
	containers, err := h.docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		for _, n := range c.Names {
			if strings.HasSuffix(strings.TrimPrefix(n, "/"), suffix) {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

// agentsPendingRestartCount returns how many agents in a crew are running on a
// stale image — i.e. the live container exists but its Image differs from the
// freshly-built `cached_image`. Returns 0 when no container exists, when the
// image already matches, or on any Docker error (the count is informational;
// surfacing a 500 here would block the whole status response).

func (h *ProvisioningHandler) agentsPendingRestartCount(ctx context.Context, crewID, slug, cachedImage string) int {
	containerID, err := h.findCrewContainer(ctx, crewID, slug)
	if err != nil || containerID == "" {
		return 0
	}
	inspect, err := h.docker.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0
	}
	if inspect.Config != nil && inspect.Config.Image == cachedImage {
		return 0
	}
	// Stale container — count active agents in this crew. We deliberately
	// count only non-deleted rows; the actual runtime impact is "all of
	// them" because they share one container, but the UI shows a number
	// that matches what the user sees in the roster.
	var count int
	if err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agents WHERE crew_id = ? AND deleted_at IS NULL`,
		crewID,
	).Scan(&count); err != nil {
		return 0
	}
	return count
}

// RestartCrewAgents destroys the crew's runtime container so the next agent
// exec recreates it from the latest `cached_image`. Returns 200 with the
// affected agent count even if no container was running (idempotent).
//
// Auth: requires "update" on the crew. Workspace scoping is enforced via the
// SELECT against crews.workspace_id.

func (h *ProvisioningHandler) RestartCrewAgents(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.docker == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "container restart not available (Docker client not configured)",
		})
		return
	}
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew ID is required")
		return
	}

	var slug string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&slug)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "query crew for restart", err)
		return
	}

	containerID, err := h.findCrewContainer(r.Context(), crewID, slug)
	if err != nil {
		replyInternalError(w, h.logger, "list containers for restart", err)
		return
	}
	if containerID == "" {
		// Nothing to restart — agents will pick up the new image on next start.
		writeJSON(w, http.StatusOK, map[string]any{"restarted": 0})
		return
	}

	// Force-remove drops the container. The next agent exec will trigger
	// EnsureCrewRuntime which re-creates from the current cached_image.
	if err := h.docker.ContainerRemove(r.Context(), containerID, container.RemoveOptions{Force: true}); err != nil {
		h.logger.Error("remove crew container", "container_id", containerID, "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to remove crew container")
		return
	}

	var restarted int
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM agents WHERE crew_id = ? AND deleted_at IS NULL`,
		crewID,
	).Scan(&restarted)

	h.logger.Info("crew runtime restarted", "crew_id", crewID, "slug", slug, "agents", restarted)
	writeJSON(w, http.StatusOK, map[string]any{"restarted": restarted})
}
