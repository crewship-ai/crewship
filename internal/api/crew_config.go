package api

import (
	"database/sql"
	"net/http"
	"time"
)

func (h *CrewHandler) ApplyAvatarStyle(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	if crewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "crewId is required"})
		return
	}

	var existingID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&existingID); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
			return
		}
		h.logger.Error("apply avatar style: lookup crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var body struct {
		AvatarStyle string `json:"avatar_style"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if body.AvatarStyle == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "avatar_style is required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET avatar_style = ?, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
		body.AvatarStyle, now, crewID)
	if err != nil {
		h.logger.Error("apply avatar style to agents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	affected, _ := res.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": affected,
		"style":   body.AvatarStyle,
	})
}
