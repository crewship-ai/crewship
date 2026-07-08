package api

// Workspace-level member role change (#867.2). Mirrors the crew-level
// CrewHandler.UpdateMemberRole but operates on the authoritative
// workspace_members.role (not a per-crew override), so it carries a
// stricter ladder: you can only grant a role BELOW your own, you cannot
// reshape a member ranked ABOVE your own, and the last OWNER cannot be
// demoted.

import (
	"database/sql"
	"net/http"

	"time"
)

type updateWorkspaceMemberRoleRequest struct {
	Role string `json:"role"`
}

// UpdateMemberRole changes one workspace member's role.
//
// PATCH /api/v1/workspaces/{workspaceId}/members/{memberId}
//
// The route is gated at MANAGER+ (roleCreate). The handler then enforces
// the ladder:
//   - caller must be MANAGER+ (defensive, in case the route gate drifts);
//   - target's CURRENT role must be strictly below the caller's — you
//     cannot modify a peer or a superior (prevents a MANAGER neutralizing
//     an OWNER);
//   - the GRANTED role must be strictly below the caller's — you cannot
//     mint a role at or above your own (no self-promotion-by-proxy, no
//     ownership minting);
//   - an OWNER being demoted must not be the last OWNER (409).
func (h *WorkspaceHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	callerRole := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	memberID := r.PathValue("memberId")

	callerUID := ""
	if user != nil {
		callerUID = user.ID
	}

	// forbid emits an RFC 7807 problem (403) while preserving the RBAC
	// audit trail replyForbidden used to write. This surface returns
	// problem+json throughout so clients get a consistent error shape.
	forbid := func(action, resource, detail string) {
		h.logger.Warn("rbac: access denied",
			"user_id", callerUID, "role", callerRole,
			"action", action, "resource", resource)
		writeProblem(w, r, http.StatusForbidden, detail)
	}

	if memberID == "" {
		writeProblem(w, r, http.StatusBadRequest, "memberId is required")
		return
	}

	// Defensive MANAGER+ floor. The route gate (roleCreate) already
	// enforces this; keep it here so the handler is safe under any wiring.
	if roleRank[callerRole] < roleRank["MANAGER"] {
		forbid("workspace_member.update_role", "workspace:"+workspaceID+"/member:"+memberID,
			"You need at least the MANAGER role to change member roles")
		return
	}

	var req updateWorkspaceMemberRoleRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if _, ok := roleRank[req.Role]; !ok {
		writeProblem(w, r, http.StatusBadRequest,
			"role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER")
		return
	}

	// Load the target's current role (scoped to this workspace).
	var currentRole string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID).Scan(&currentRole)
	if err == sql.ErrNoRows {
		writeProblem(w, r, http.StatusNotFound, "Member not found")
		return
	}
	if err != nil {
		h.logger.Error("lookup workspace member for role update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Cannot reshape a member ranked above your own.
	if roleRank[currentRole] > roleRank[callerRole] {
		forbid("workspace_member.update_role",
			"workspace:"+workspaceID+"/member:"+memberID+"/current:"+currentRole,
			"You cannot modify a member ranked above your own role")
		return
	}

	// Cannot grant a role at or above your own (ladder ceiling).
	if roleRank[req.Role] >= roleRank[callerRole] {
		forbid("workspace_member.grant_role",
			"workspace:"+workspaceID+"/member:"+memberID+"/role:"+req.Role,
			"You cannot grant a role at or above your own")
		return
	}

	// Last-OWNER guard, folded INTO the UPDATE so the owner-count check and
	// the write are a single atomic statement — two concurrent demotions
	// can't both observe >1 owner and leave the workspace with none. The
	// row exists (verified above), so RowsAffected == 0 means the guard
	// subquery blocked a last-owner demotion → 409.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE workspace_members
		SET role = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
		  AND NOT (
		    role = 'OWNER'
		    AND ? != 'OWNER'
		    AND (SELECT COUNT(*) FROM workspace_members
		         WHERE workspace_id = ? AND role = 'OWNER') <= 1
		  )`,
		req.Role, now, memberID, workspaceID, req.Role, workspaceID)
	if err != nil {
		h.logger.Error("update workspace member role", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("rows affected for member role update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusConflict, "Cannot demote the last owner of the workspace")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "updated",
		"member_id": memberID,
		"role":      req.Role,
	})
}
