package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/presence"
)

// PresenceHandler serves the Watch Roster read endpoints. Writes live on
// the agent runtime side (orchestrator + sidecar), so this handler has
// no POST path — status is owned by the system, not by operators.
type PresenceHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewPresenceHandler(db *sql.DB, logger *slog.Logger) *PresenceHandler {
	return &PresenceHandler{db: db, logger: logger}
}

// rosterRow is the JSON shape the Watch Roster UI (and the
// `crewship presence roster` CLI) expects. We flatten presence.Snapshot
// into the external contract so future internal changes (adding fields,
// renaming them) don't leak into the API.
type rosterRow struct {
	AgentID string         `json:"agent_id"`
	CrewID  string         `json:"crew_id,omitempty"`
	Status  string         `json:"status"`
	Since   time.Time      `json:"since"`
	Details map[string]any `json:"details,omitempty"`
}

// Roster serves GET /api/v1/presence/roster[?crew_id=...].
// Workspace-scoped — the caller's workspace context is the primary
// filter. An optional ?crew_id narrows further.
func (h *PresenceHandler) Roster(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	crewID := r.URL.Query().Get("crew_id")
	if crewID != "" {
		ok, err := crewBelongsToWorkspace(r.Context(), h.db, crewID, workspaceID)
		if err != nil {
			h.logger.Error("presence roster: crew lookup failed", "err", err, "crew_id", crewID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "crew lookup failed"})
			return
		}
		if !ok {
			// Cross-tenant crew lookup → flat 404 so existence doesn't leak
			// across workspaces.
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
			return
		}
	}

	snaps, err := presence.ListByWorkspace(r.Context(), h.db, workspaceID)
	if err != nil {
		h.logger.Error("presence roster", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}

	out := make([]rosterRow, 0, len(snaps))
	for _, s := range snaps {
		if crewID != "" && s.CrewID != crewID {
			continue
		}
		out = append(out, rosterRow{
			AgentID: s.AgentID,
			CrewID:  s.CrewID,
			Status:  string(s.Status),
			Since:   s.Since,
			Details: s.Details,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rows":  out,
		"count": len(out),
	})
}
