package api

// Crew runtime container restart endpoint + small helpers used by
// ProvisionStatus to report agents_pending_restart. Extracted from
// crew_provisioning.go.

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/docker/docker/api/types/container"
)

const crewContainerPrefix = "crewship-team-"

// findCrewContainer returns the live Docker container ID for the given crew
// slug, or "" if no container is running. Returns an error only on a real
// Docker failure — "no container found" is the empty-string success path.

func (h *ProvisioningHandler) findCrewContainer(ctx context.Context, slug string) (string, error) {
	if h.docker == nil {
		return "", nil
	}
	name := crewContainerPrefix + slug
	containers, err := h.docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		for _, n := range c.Names {
			if n == "/"+name {
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
	containerID, err := h.findCrewContainer(ctx, slug)
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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crew ID is required"})
		return
	}

	var slug string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		crewID, workspaceID,
	).Scan(&slug)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
		return
	}
	if err != nil {
		h.logger.Error("query crew for restart", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	containerID, err := h.findCrewContainer(r.Context(), slug)
	if err != nil {
		h.logger.Error("list containers for restart", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
