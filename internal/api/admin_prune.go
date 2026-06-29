package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/provider"
)

// LegacyResourceHandler detects and removes pre-C1 (slug-only) crew runtime
// resources that survive a DB nuke+reseed and otherwise block agent container
// start. The docker provider's checkNoLegacyCrewResources only DETECTS them at
// provision time (and the failure reaches the user as a generic "failed to
// start agent container"); without a remediation path an operator is stuck (no
// CLI for `docker volume rm`, host SSH off-limits). This handler is that path.
//
// Both endpoints operate INSTANCE-WIDE: legacy docker names carry no workspace
// or crew id, so detection (what `crewship doctor` surfaces) and prune (the
// remediation) must enumerate the same full crew set — otherwise doctor could
// WARN on a slug the prune command can never reach. Only orphaned legacy names
// are ever touched; the id-scoped resources the live runtime uses are excluded
// in the provider (slug/id-collision safe).
type LegacyResourceHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// pruner/detector are nil when the active container provider can't act on
	// them (e.g. a non-docker runtime); the endpoints then 503 rather than lie.
	pruner   provider.LegacyResourcePruner
	detector provider.LegacyResourceDetector
}

func NewLegacyResourceHandler(db *sql.DB, logger *slog.Logger, pruner provider.LegacyResourcePruner, detector provider.LegacyResourceDetector) *LegacyResourceHandler {
	return &LegacyResourceHandler{db: db, logger: logger, pruner: pruner, detector: detector}
}

type legacyPruneResponse struct {
	Removed []string `json:"removed"`
	Count   int      `json:"count"`
}

type legacyDetectResponse struct {
	Present bool `json:"present"`
}

// allCrewRefs enumerates every live crew on the instance as (id, slug). The id
// is carried so the provider can PROTECT live id-scoped resources from a
// slug/id collision when matching the slug-only legacy names.
func (h *LegacyResourceHandler) allCrewRefs(ctx context.Context) ([]provider.CrewRef, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id, slug FROM crews WHERE deleted_at IS NULL`)
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

// Detect reports whether the daemon carries orphaned pre-C1 legacy resources.
// Admin-only, read-only. 503 when the provider can't detect (non-docker).
func (h *LegacyResourceHandler) Detect(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canRole(RoleFromContext(ctx), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if WorkspaceIDFromContext(ctx) == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.detector == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "legacy detection unavailable: docker not configured",
		})
		return
	}
	crews, err := h.allCrewRefs(ctx)
	if err != nil {
		h.logger.Error("legacy detect: list crews", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	present, err := h.detector.HasLegacyCrewResources(ctx, crews)
	if err != nil {
		h.logger.Error("legacy detect: scan", "error", err)
		replyError(w, http.StatusInternalServerError, "legacy detection failed")
		return
	}
	writeJSON(w, http.StatusOK, legacyDetectResponse{Present: present})
}

// Prune removes orphaned pre-C1 legacy resources instance-wide. Admin-only.
// 503 when the provider can't prune. On a mid-prune failure it returns 500 WITH
// the partial removed list so the operator (who works through the CLI, not the
// server logs) can reconcile a partially-mutated docker state.
func (h *LegacyResourceHandler) Prune(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canRole(RoleFromContext(ctx), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if WorkspaceIDFromContext(ctx) == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	if h.pruner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "legacy prune unavailable: docker not configured",
		})
		return
	}
	crews, err := h.allCrewRefs(ctx)
	if err != nil {
		h.logger.Error("legacy prune: list crews", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	removed, err := h.pruner.PruneLegacyCrewResources(ctx, crews)
	if removed == nil {
		removed = []string{}
	}
	if err != nil {
		h.logger.Error("legacy prune: pipeline", "error", err, "removed", removed)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":   "legacy prune failed",
			"removed": removed,
			"count":   len(removed),
		})
		return
	}
	h.logger.Info("legacy prune complete", "removed", removed)
	writeJSON(w, http.StatusOK, legacyPruneResponse{Removed: removed, Count: len(removed)})
}
