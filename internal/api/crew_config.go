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
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	var existingID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&existingID); err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("apply avatar style: lookup crew", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var body struct {
		AvatarStyle    string `json:"avatar_style"`
		ResetOverrides bool   `json:"reset_overrides"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if !body.ResetOverrides && body.AvatarStyle == "" {
		replyError(w, http.StatusBadRequest, "avatar_style is required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	var res sql.Result
	var err error
	if body.ResetOverrides {
		res, err = h.db.ExecContext(r.Context(),
			"UPDATE agents SET avatar_style = NULL, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
			now, crewID)
	} else {
		res, err = h.db.ExecContext(r.Context(),
			"UPDATE agents SET avatar_style = ?, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
			body.AvatarStyle, now, crewID)
	}
	if err != nil {
		h.logger.Error("apply avatar style to agents", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	affected, _ := res.RowsAffected()
	response := map[string]any{"updated": affected}
	if body.ResetOverrides {
		response["reset"] = true
	} else {
		response["style"] = body.AvatarStyle
	}
	writeJSON(w, http.StatusOK, response)
}
