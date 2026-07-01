package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// CrewRuntimeHandler tears down the docker runtime (containers + volumes) of
// every crew in the CONTEXT workspace, WITHOUT removing cached devcontainer
// images. It's the docker half of a full workspace teardown: seed --nuke wipes
// the DB rows, this clears the orphaned container/volume state those crews left
// behind (crew deletion is a DB soft-delete that never touched docker). Cached
// images are preserved so a reseed doesn't force a rebuild.
//
// Workspace-scoped by design — unlike the instance-wide legacy pruner: a nuke
// resets ONE workspace, so only that workspace's crews are enumerated (a
// host-wide teardown would take down sibling instances/workspaces sharing the
// daemon). Admin-only. A nil pruner (non-docker provider) 503s rather than lie.
type CrewRuntimeHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// pruner is nil when the active container provider can't act on docker
	// runtimes (e.g. a non-docker runtime); the endpoint then 503s.
	pruner provider.CrewRuntimePruner
}

func NewCrewRuntimeHandler(db *sql.DB, logger *slog.Logger, pruner provider.CrewRuntimePruner) *CrewRuntimeHandler {
	return &CrewRuntimeHandler{db: db, logger: logger, pruner: pruner}
}

type crewRuntimePruneResponse struct {
	Removed []string `json:"removed"`
	Count   int      `json:"count"`
}

// workspaceCrewRefs enumerates one workspace's live crews as (id, slug). Nuke
// deletes the crew ROWS via the API AFTER this teardown, so at call time the
// crews still exist (deleted_at IS NULL) and carry the id/slug the pruner needs
// to build id-scoped docker names.
func (h *CrewRuntimeHandler) workspaceCrewRefs(ctx context.Context, workspaceID string) ([]provider.CrewRef, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var crews []provider.CrewRef
	for rows.Next() {
		var c provider.CrewRef
		if err := rows.Scan(&c.ID, &c.Slug); err != nil {
			return nil, err
		}
		crews = append(crews, c)
	}
	return crews, rows.Err()
}

// Prune removes every crew's docker container(s)+volumes for the context
// workspace. Admin-only. 503 when docker isn't configured. On a mid-prune
// failure it returns 500 WITH the partial removed list so the operator (who
// works through the CLI, not the server logs) can reconcile a partially-mutated
// docker state — mirroring LegacyResourceHandler.Prune.
func (h *CrewRuntimeHandler) Prune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canRole(RoleFromContext(ctx), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	workspaceID := WorkspaceIDFromContext(ctx)
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.pruner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "crew runtime prune unavailable: docker not configured",
		})
		return
	}
	crews, err := h.workspaceCrewRefs(ctx, workspaceID)
	if err != nil {
		h.logger.Error("crew runtime prune: list crews", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	removed, err := h.pruner.PruneCrewRuntimes(ctx, crews)
	if removed == nil {
		removed = []string{}
	}
	if err != nil {
		h.logger.Error("crew runtime prune", "error", err, "removed", removed)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "crew runtime prune failed",
			"removed": removed,
			"count":   len(removed),
		})
		return
	}
	h.logger.Info("crew runtime prune complete", "workspace", workspaceID, "removed", removed)
	writeJSON(w, http.StatusOK, crewRuntimePruneResponse{Removed: removed, Count: len(removed)})
}
