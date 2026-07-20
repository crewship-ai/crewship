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
		replyInternalError(w, h.logger, "apply avatar style: lookup crew", err)
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
	// Clearing avatar_svg/_hash alongside the style is not optional: those
	// columns hold a render of the *old* style (#1297), so leaving them
	// would make this endpoint a no-op for every agent that has been
	// backfilled — the crew's style would change in the DB while the
	// roster kept serving the previous faces.
	if body.ResetOverrides {
		res, err = h.db.ExecContext(r.Context(),
			"UPDATE agents SET avatar_style = NULL, avatar_svg = NULL, avatar_svg_hash = NULL, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
			now, crewID)
	} else {
		res, err = h.db.ExecContext(r.Context(),
			"UPDATE agents SET avatar_style = ?, avatar_svg = NULL, avatar_svg_hash = NULL, updated_at = ? WHERE crew_id = ? AND deleted_at IS NULL",
			body.AvatarStyle, now, crewID)
	}
	if err != nil {
		replyInternalError(w, h.logger, "apply avatar style to agents", err)
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
