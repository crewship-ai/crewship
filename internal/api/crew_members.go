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
	Role      *string     `json:"role,omitempty"` // per-crew role override; nil = inherit workspace role
	CreatedAt string      `json:"created_at"`
	User      *memberUser `json:"user,omitempty"`
}

type addCrewMemberRequest struct {
	UserID string `json:"user_id"`
	// Role optionally elevates the member's effective role inside
	// this crew above their workspace role. NEVER below — the
	// effective-role helper takes max(workspace, crew). Empty means
	// "inherit workspace role" which is the historical behaviour.
	Role string `json:"role,omitempty"`
}

type updateCrewMemberRoleRequest struct {
	// Role on a PATCH membership endpoint. Empty string drops the
	// override back to "inherit workspace role" (the column goes
	// NULL); a named role pins the elevation.
	Role string `json:"role"`
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
		SELECT cm.id, cm.crew_id, cm.user_id, cm.role, cm.created_at,
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
		var roleOverride sql.NullString
		if err := rows.Scan(&m.ID, &m.CrewID, &m.UserID, &roleOverride, &m.CreatedAt,
			&u.ID, &u.Email, &u.FullName, &u.AvatarURL); err != nil {
			h.logger.Error("scan crew member", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if roleOverride.Valid {
			rv := roleOverride.String
			m.Role = &rv
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

	// Optional per-crew role override. Validated here so an invalid
	// role string never makes it into the DB even with the CHECK
	// constraint in place — a clean 400 beats a sql.Exec error.
	var roleParam sql.NullString
	if req.Role != "" {
		if _, ok := roleRank[req.Role]; !ok {
			replyError(w, http.StatusBadRequest,
				"role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER")
			return
		}
		// Role-grant ceiling (A1): a caller may never grant a per-crew
		// role that outranks their own effective role. canRole(...,
		// "create") above only proves the caller is MANAGER+, which
		// would otherwise let a MANAGER ladder a member straight to
		// OWNER and bypass the workspace gate. This mirrors the gate
		// UpdateMemberRole enforces on promotion/demotion.
		if roleRank[req.Role] > roleRank[role] {
			var uid string
			if u := UserFromContext(r.Context()); u != nil {
				uid = u.ID
			}
			replyForbidden(w, h.logger, uid, role,
				"crew_member.grant_role",
				"crew:"+crewID+"/role:"+req.Role)
			return
		}
		roleParam = sql.NullString{String: req.Role, Valid: true}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	memberID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO crew_members (id, crew_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, crewID, req.UserID, roleParam, now)
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

// UpdateMemberRole sets or clears the per-crew role override
// (PATCH /api/v1/crews/{crewId}/members/{memberId}). The post-
// Patch-M1 contract: an empty/omitted body.Role drops the override
// back to NULL (member inherits workspace role); a named role pins
// the elevation. Workspace ADMIN/OWNER required (or the per-crew
// elevation must already grant create — but only OWNER/ADMIN can
// reshape membership permissions, MANAGER cannot promote a peer to
// their own level let alone above it).
func (h *CrewHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	wsRole := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	crewID := r.PathValue("crewId")
	memberID := r.PathValue("memberId")

	// Promotion / demotion are admin-only — even a per-crew ADMIN
	// can't reshape peer permissions because they could ladder a
	// MEMBER straight to OWNER and bypass the workspace gate. The
	// workspace ADMIN/OWNER gate is the only one that's safe for
	// this surface.
	if wsRole != "OWNER" && wsRole != "ADMIN" {
		var uid, urole string
		if user != nil {
			uid = user.ID
		}
		urole = wsRole
		replyForbidden(w, h.logger, uid, urole,
			"crew_member.update_role",
			"crew:"+crewID+"/member:"+memberID)
		return
	}

	if crewID == "" || memberID == "" {
		replyError(w, http.StatusBadRequest, "crewId and memberId are required")
		return
	}

	// Verify crew exists in this workspace + membership exists in
	// that crew. Doing both in one query keeps the error path tight.
	var existingMemberID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT cm.id FROM crew_members cm
		JOIN crews c ON c.id = cm.crew_id
		WHERE cm.id = ? AND cm.crew_id = ?
		  AND c.workspace_id = ? AND c.deleted_at IS NULL
	`, memberID, crewID, workspaceID).Scan(&existingMemberID)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Crew member not found")
			return
		}
		h.logger.Error("lookup crew member for role update", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var req updateCrewMemberRoleRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	var roleParam sql.NullString
	if req.Role != "" {
		if _, ok := roleRank[req.Role]; !ok {
			replyError(w, http.StatusBadRequest,
				"role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER, or empty to clear")
			return
		}
		// Role-grant ceiling (A1): the OWNER/ADMIN gate above proves the
		// caller can reshape membership, but not that they may mint a
		// role above their own — an ADMIN must not be able to pin a
		// member to OWNER. Cap the granted role at the caller's effective
		// role, same as AddMember.
		if roleRank[req.Role] > roleRank[wsRole] {
			var uid string
			if user != nil {
				uid = user.ID
			}
			replyForbidden(w, h.logger, uid, wsRole,
				"crew_member.grant_role",
				"crew:"+crewID+"/member:"+memberID+"/role:"+req.Role)
			return
		}
		roleParam = sql.NullString{String: req.Role, Valid: true}
	}

	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE crew_members SET role = ? WHERE id = ?", roleParam, memberID); err != nil {
		h.logger.Error("update crew member role", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "updated",
		"role":   req.Role, // empty string echoes "cleared override"
	})
}
