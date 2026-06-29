package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// LegacyPruneHandler removes pre-C1 (slug-only) crew runtime resources that
// survive a DB nuke+reseed and otherwise block agent container start. The
// docker provider's checkNoLegacyCrewResources only DETECTS them — and the
// failure reaches the user as a generic "failed to start agent container" via
// chatbridge — so without a remediation path an operator is stuck (no CLI for
// `docker volume rm`, and SSHing the host is off-limits). This handler is that
// path. Only the orphaned legacy names are touched; the id-scoped resources the
// live runtime uses are never removed (enforced in PruneLegacyCrewResources).
type LegacyPruneHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// pruner is nil when the active container provider can't prune (e.g. a
	// non-docker runtime); the endpoint then 503s rather than lying.
	pruner provider.LegacyResourcePruner
}

func NewLegacyPruneHandler(db *sql.DB, logger *slog.Logger, pruner provider.LegacyResourcePruner) *LegacyPruneHandler {
	return &LegacyPruneHandler{db: db, logger: logger, pruner: pruner}
}

type legacyPruneResponse struct {
	Removed []string `json:"removed"`
	Count   int      `json:"count"`
}

// Prune removes legacy slug-only docker resources for every crew slug in the
// caller's workspace. Admin-only (manage), matching the rest of the admin
// surface. 503 when the active container provider can't prune. The legacy
// names carry no crew id, so enumerating the caller's slugs is enough to clear
// the standard demo crews (engineering/quality/ops) — and it keeps the blast
// radius inside the caller's own workspace.
func (h *LegacyPruneHandler) Prune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.pruner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "legacy prune unavailable: docker not configured",
		})
		return
	}

	// All crews in the workspace, including soft-deleted ones: a crew deleted
	// after a pre-C1 provision is exactly the case that orphans legacy
	// resources, so we still want its slug checked.
	rows, err := h.db.QueryContext(ctx, `SELECT DISTINCT slug FROM crews WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		h.logger.Error("legacy prune: list crew slugs", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			h.logger.Error("legacy prune: scan slug", "error", err)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("legacy prune: rows", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	removed, err := h.pruner.PruneLegacyCrewResources(ctx, slugs)
	if err != nil {
		// Partial removal is possible (a transport failure mid-prune); log
		// what got cleared so the operator can reconcile.
		h.logger.Error("legacy prune: pipeline", "error", err, "removed", removed)
		replyError(w, http.StatusInternalServerError, "legacy prune failed")
		return
	}
	if removed == nil {
		removed = []string{}
	}
	h.logger.Info("legacy prune complete", "workspace_id", workspaceID, "removed", removed)
	writeJSON(w, http.StatusOK, legacyPruneResponse{Removed: removed, Count: len(removed)})
}
