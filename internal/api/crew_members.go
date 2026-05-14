package api

import (
	"database/sql"
	"net/http"
	"time"
)

type crewMemberResponse struct {
	ID        string      `json:"id"`
	CrewID    string      `json:"crew_id"`
	UserID    string      `json:"user_id"`
	CreatedAt string      `json:"created_at"`
	User      *memberUser `json:"user,omitempty"`
}

type addCrewMemberRequest struct {
	UserID string `json:"user_id"`
}

func (h *CrewHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Verify crew exists and belongs to workspace
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&existingID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for list members", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cm.id, cm.crew_id, cm.user_id, cm.created_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM crew_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.crew_id = ?
		ORDER BY cm.created_at ASC
	`, crewID)
	if err != nil {
		h.logger.Error("list crew members", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []crewMemberResponse
	for rows.Next() {
		var m crewMemberResponse
		var u memberUser
		if err := rows.Scan(&m.ID, &m.CrewID, &m.UserID, &m.CreatedAt,
			&u.ID, &u.Email, &u.FullName, &u.AvatarURL); err != nil {
			h.logger.Error("scan crew member", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		m.User = &u
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (crew members)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []crewMemberResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *CrewHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")

	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Verify crew exists and belongs to workspace
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&existingID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for add member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var req addCrewMemberRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.UserID == "" {
		replyError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	// Check user is a workspace member
	var wsMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, req.UserID).Scan(&wsMemberID)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusBadRequest, "User is not a member of this workspace")
		return
	}
	if err != nil {
		h.logger.Error("check workspace membership", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Check not already a crew member
	var existingMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crew_members WHERE crew_id = ? AND user_id = ?",
		crewID, req.UserID).Scan(&existingMemberID)
	if err == nil {
		replyError(w, http.StatusConflict, "User is already a member of this crew")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check crew membership", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	memberID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO crew_members (id, crew_id, user_id, created_at) VALUES (?, ?, ?, ?)",
		memberID, crewID, req.UserID, now)
	if err != nil {
		h.logger.Error("insert crew member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Return member with user info
	var m crewMemberResponse
	var u memberUser
	err = h.db.QueryRowContext(r.Context(), `
		SELECT cm.id, cm.crew_id, cm.user_id, cm.created_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM crew_members cm
		JOIN users u ON u.id = cm.user_id
		WHERE cm.id = ?
	`, memberID).Scan(&m.ID, &m.CrewID, &m.UserID, &m.CreatedAt,
		&u.ID, &u.Email, &u.FullName, &u.AvatarURL)
	if err != nil {
		h.logger.Error("get crew member after insert", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.User = &u

	writeJSON(w, http.StatusCreated, m)
}

func (h *CrewHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	crewID := r.PathValue("crewId")
	memberID := r.PathValue("memberId")

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}
	if memberID == "" {
		replyError(w, http.StatusBadRequest, "memberId is required")
		return
	}

	// Verify crew exists and belongs to workspace
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&existingID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for remove member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Verify member exists in this crew
	var existingMemberID string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crew_members WHERE id = ? AND crew_id = ?",
		memberID, crewID).Scan(&existingMemberID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew member not found")
			return
		}
		h.logger.Error("get crew member for remove", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		"DELETE FROM crew_members WHERE id = ? AND crew_id = ?",
		memberID, crewID)
	if err != nil {
		h.logger.Error("delete crew member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
