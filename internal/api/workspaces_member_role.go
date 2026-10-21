package api

// Workspace-level member role change (#867.2). Mirrors the crew-level
// CrewHandler.UpdateMemberRole but operates on the authoritative
// workspace_members.role (not a per-crew override), so it carries a
// stricter ladder: you can only grant a role BELOW your own, you cannot
// reshape a member ranked ABOVE your own, and the last OWNER cannot be
// demoted.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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

	fail := func(what string, err error) {
		h.logger.Error(what, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
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

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail("begin member role mutation", err)
		return
	}
	defer tx.Rollback()
	// Recheck after the body was read, on the same writer as the role and
	// its event. A stale middleware role cannot authorize a new mutation.
	err = tx.QueryRowContext(r.Context(), `SELECT m.role FROM workspace_members m
JOIN users u ON u.id=m.user_id AND u.suspended_at IS NULL
JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL
WHERE m.workspace_id=? AND m.user_id=? AND NOT EXISTS(
 SELECT 1 FROM workspace_members guest WHERE guest.user_id=m.user_id AND guest.access_mode='restricted')`, workspaceID, callerUID).Scan(&callerRole)
	if errors.Is(err, sql.ErrNoRows) {
		forbid("workspace_member.update_role", "workspace:"+workspaceID,
			"Active trusted workspace membership is required")
		return
	}
	if err != nil {
		fail("read current role administrator", err)
		return
	}
	if roleRank[callerRole] < roleRank["MANAGER"] {
		forbid("workspace_member.update_role", "workspace:"+workspaceID,
			"You need at least the MANAGER role to change member roles")
		return
	}

	// Load the target's current role (scoped to this workspace). The user id
	// comes along for the audit row: a membership id names nobody, a user id
	// resolves to an email.
	var currentRole, memberUserID string
	err = tx.QueryRowContext(r.Context(),
		"SELECT role, user_id FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID).Scan(&currentRole, &memberUserID)
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
	res, err := tx.ExecContext(r.Context(), `
		UPDATE workspace_members
		SET role = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ?
		  AND (? NOT IN ('ADMIN','OWNER') OR NOT EXISTS (SELECT 1 FROM workspace_members guest WHERE guest.user_id=workspace_members.user_id AND guest.access_mode='restricted'))
		  AND NOT (
		    role = 'OWNER'
		    AND ? != 'OWNER'
		    AND (SELECT COUNT(*) FROM workspace_members
		         WHERE workspace_id = ? AND role = 'OWNER') <= 1
		  )`,
		req.Role, now, memberID, workspaceID, req.Role, req.Role, workspaceID)
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
		writeProblem(w, r, http.StatusConflict, "Cannot demote the last owner or promote a Guest to administrator; restore trusted access explicitly first")
		return
	}

	// Settings Audit still reads audit_logs (People filters, CSV and
	// IP/UA expansion). Keep its existing projection atomic with the
	// canonical journal; auditFromRequest is deliberately best effort.
	auditedUser := memberUserID
	if auditedUser == "" {
		auditedUser = memberID
	}
	metadata, err := json.Marshal(map[string]string{"from": currentRole, "to": req.Role, "member_id": memberID})
	if err != nil {
		fail("encode member role settings audit", err)
		return
	}
	auditID := generateCUID()
	res, err = tx.ExecContext(r.Context(), `INSERT INTO audit_logs
(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at)
VALUES(?,?,?,'member.role_change','WorkspaceMember',?,?,?)`, auditID, workspaceID, callerUID, auditedUser, string(metadata), now)
	if err != nil {
		fail("write member role settings audit", err)
		return
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		fail("acknowledge member role settings audit insert", errors.Join(err, errors.New("settings audit insert did not affect one row")))
		return
	}

	notify, err := appendAuthorizationAudit(r.Context(), tx, h.policyJournal, journal.Entry{
		WorkspaceID: workspaceID, Type: journal.EntryMemberRoleChanged,
		ActorType: journal.ActorUser, ActorID: callerUID, Summary: "Workspace member role changed",
		Payload: map[string]any{"subject_user_id": memberUserID, "member_id": memberID,
			"action": "role_changed", "previous_role": currentRole, "role": req.Role},
	})
	if err != nil {
		fail("audit member role mutation", err)
		return
	}
	// Read back after BOTH inserts: neither audit trigger may remove or
	// alter the legacy projection, or undo the role before commit.
	var savedWorkspace, savedActor, savedAction, savedKind, savedEntity, savedMetadata, savedCreated string
	var savedIP, savedUA sql.NullString
	if err := tx.QueryRowContext(r.Context(), `SELECT workspace_id,user_id,action,entity_type,entity_id,metadata,created_at,ip_address,user_agent FROM audit_logs WHERE id=?`, auditID).Scan(
		&savedWorkspace, &savedActor, &savedAction, &savedKind, &savedEntity, &savedMetadata, &savedCreated, &savedIP, &savedUA); err != nil {
		fail("acknowledge member role settings audit", err)
		return
	}
	if savedWorkspace != workspaceID || savedActor != callerUID || savedAction != "member.role_change" || savedKind != "WorkspaceMember" || savedEntity != auditedUser || savedMetadata != string(metadata) || savedCreated != now || savedIP.Valid || savedUA.Valid {
		fail("acknowledge member role settings audit", errors.New("settings audit projection did not persist"))
		return
	}
	// A trigger on the event must not undo the mutation after its first
	// acknowledgement. Check the final state before committing either row.
	var savedRole, savedUser, savedAt string
	if err := tx.QueryRowContext(r.Context(), `SELECT role,user_id,updated_at FROM workspace_members WHERE id=? AND workspace_id=?`, memberID, workspaceID).Scan(&savedRole, &savedUser, &savedAt); err != nil {
		fail("acknowledge member role", err)
		return
	}
	if savedRole != req.Role || savedUser != memberUserID || savedAt != now {
		fail("acknowledge member role", errors.New("member role mutation did not persist"))
		return
	}
	if err := tx.Commit(); err != nil {
		fail("commit member role mutation", err)
		return
	}
	if notify != nil {
		notify()
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "updated",
		"member_id": memberID,
		"role":      req.Role,
	})
}
