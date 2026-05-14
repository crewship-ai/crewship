package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/consolidate"
)

// MemoryHealthHandler serves the 5-metric memory health dashboard.
// Scope narrows by ?crew_id=... query param; omit for a workspace-
// wide view. Reads are cheap (five aggregate SQL queries) so we
// compute fresh on every request instead of always hitting the
// persisted snapshot table — the persisted snapshots exist for
// time-series plots, not real-time reads.
type MemoryHealthHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewMemoryHealthHandler(db *sql.DB, logger *slog.Logger) *MemoryHealthHandler {
	return &MemoryHealthHandler{db: db, logger: logger}
}

// Get serves GET /api/v1/memory/health[?crew_id=...]. Returns the
// computed score plus metric breakdown. Every workspace member can
// read — the output contains no sensitive data (counts + ratios
// only, no raw entry content).
func (h *MemoryHealthHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	crewID := r.URL.Query().Get("crew_id")
	if crewID != "" {
		ok, err := crewBelongsToWorkspace(r.Context(), h.db, crewID, workspaceID)
		if err != nil {
			h.logger.Error("memory health: crew lookup", "err", err)
			replyError(w, http.StatusInternalServerError, "crew lookup failed")
			return
		}
		if !ok {
			replyError(w, http.StatusNotFound, "crew not found")
			return
		}
	}
	snap, err := consolidate.ComputeHealth(r.Context(), h.db, workspaceID, crewID)
	if err != nil {
		h.logger.Error("memory health: compute", "err", err)
		replyError(w, http.StatusInternalServerError, "compute failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": snap.WorkspaceID,
		"crew_id":      snap.CrewID,
		"computed_at":  snap.ComputedAt,
		"overall":      snap.Overall,
		"metrics": map[string]float64{
			"freshness":    snap.Freshness,
			"coverage":     snap.Coverage,
			"coherence":    snap.Coherence,
			"efficiency":   snap.Efficiency,
			"reachability": snap.Reachability,
		},
		"details": snap.Details,
	})
}
