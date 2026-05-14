package api

// Workspace membership + invitation surfaces. Members live in
// workspace_members; invitations issue a token-gated link the
// recipient redeems via /api/v1/auth. Extracted from workspaces.go
// for readability.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
)

type memberResponse struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	UserID      string      `json:"user_id"`
	Role        string      `json:"role"`
	CreatedAt   string      `json:"created_at"`
	UpdatedAt   string      `json:"updated_at"`
	User        *memberUser `json:"user,omitempty"`
}

type memberUser struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	FullName  *string `json:"full_name"`
	AvatarURL *string `json:"avatar_url"`
}

// ListMembers returns all members of the workspace with their roles and user details.
// GET /api/v1/workspaces/{workspaceId}/members

func (h *WorkspaceHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wm.id, wm.workspace_id, wm.user_id, wm.role, wm.created_at, wm.updated_at,
			u.id, u.email, u.full_name, u.avatar_url
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ?
		ORDER BY wm.created_at ASC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list members", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []memberResponse
	for rows.Next() {
		var m memberResponse
		var u memberUser
		if err := rows.Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.Role, &m.CreatedAt, &m.UpdatedAt,
			&u.ID, &u.Email, &u.FullName, &u.AvatarURL); err != nil {
			h.logger.Error("scan member", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		m.User = &u
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (members)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []memberResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// AddMember adds a user to the workspace with a specified role.
// POST /api/v1/workspaces/{workspaceId}/members

func (h *WorkspaceHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if h.license != nil {
		if err := h.license.CheckMemberLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				replyError(w, http.StatusPaymentRequired, err.Error())
				return
			}
			h.logger.Error("check member limit", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	var req addMemberRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.UserID == "" {
		replyError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if req.Role == "" {
		req.Role = "MEMBER"
	}

	// V-02: Validate role against whitelist and prevent escalation
	validAssignableRoles := map[string]bool{"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}
	if !validAssignableRoles[req.Role] {
		replyError(w, http.StatusBadRequest, "role must be ADMIN, MANAGER, MEMBER, or VIEWER")
		return
	}
	// Only OWNER can assign ADMIN role
	if req.Role == "ADMIN" && role != "OWNER" {
		replyError(w, http.StatusForbidden, "Only workspace owner can assign ADMIN role")
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		workspaceID, req.UserID).Scan(&existingID)
	if err == nil {
		replyError(w, http.StatusConflict, "User is already a member of this workspace")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var userExists bool
	err = h.db.QueryRowContext(r.Context(), "SELECT 1 FROM users WHERE id = ?", req.UserID).Scan(&userExists)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusNotFound, "User not found")
		return
	}
	if err != nil {
		h.logger.Error("check user exists", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	memberID := generateCUID()

	_, err = h.db.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		memberID, workspaceID, req.UserID, req.Role, now, now)
	if err != nil {
		h.logger.Error("insert member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, memberResponse{
		ID:          memberID,
		WorkspaceID: workspaceID,
		UserID:      req.UserID,
		Role:        req.Role,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

// RemoveMember removes a user from the workspace (owners cannot remove themselves).
// DELETE /api/v1/workspaces/{workspaceId}/members/{memberId}

func (h *WorkspaceHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	memberID := r.PathValue("memberId")

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if memberID == "" {
		replyError(w, http.StatusBadRequest, "memberId is required")
		return
	}

	var memberRole string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID).Scan(&memberRole)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Member not found")
			return
		}
		h.logger.Error("get member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if memberRole == "OWNER" {
		replyError(w, http.StatusForbidden, "Cannot remove workspace owner")
		return
	}

	_, err = h.db.ExecContext(r.Context(),
		"DELETE FROM workspace_members WHERE id = ? AND workspace_id = ?",
		memberID, workspaceID)
	if err != nil {
		h.logger.Error("delete member", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

type invitationResponse struct {
	ID          string       `json:"id"`
	WorkspaceID string       `json:"workspace_id"`
	Email       string       `json:"email"`
	Role        string       `json:"role"`
	InvitedBy   string       `json:"invited_by"`
	Token       string       `json:"token"`
	ExpiresAt   string       `json:"expires_at"`
	AcceptedAt  *string      `json:"accepted_at"`
	CreatedAt   string       `json:"created_at"`
	Inviter     *inviterUser `json:"inviter,omitempty"`
}

type inviterUser struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	FullName *string `json:"full_name"`
}

// ListInvitations returns all pending invitations for the workspace.
// GET /api/v1/workspaces/{workspaceId}/invitations

func (h *WorkspaceHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wi.id, wi.workspace_id, wi.email, wi.role, wi.invited_by, wi.token,
			wi.expires_at, wi.accepted_at, wi.created_at,
			u.id, u.email, u.full_name
		FROM workspace_invitations wi
		JOIN users u ON u.id = wi.invited_by
		WHERE wi.workspace_id = ? AND wi.accepted_at IS NULL
		ORDER BY wi.created_at DESC
	`, workspaceID)
	if err != nil {
		h.logger.Error("list invitations", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []invitationResponse
	for rows.Next() {
		var inv invitationResponse
		var inviter inviterUser
		if err := rows.Scan(&inv.ID, &inv.WorkspaceID, &inv.Email, &inv.Role,
			&inv.InvitedBy, &inv.Token, &inv.ExpiresAt, &inv.AcceptedAt, &inv.CreatedAt,
			&inviter.ID, &inviter.Email, &inviter.FullName); err != nil {
			h.logger.Error("scan invitation", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		inv.Inviter = &inviter
		result = append(result, inv)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (invitations)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []invitationResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

type createInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generateToken: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateInvitation creates a new invitation link for the workspace.
// POST /api/v1/workspaces/{workspaceId}/invitations

func (h *WorkspaceHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	if h.license != nil {
		if err := h.license.CheckMemberLimit(r.Context(), h.db, workspaceID); err != nil {
			if license.IsLimitError(err) {
				replyError(w, http.StatusPaymentRequired, err.Error())
				return
			}
			h.logger.Error("check member limit for invitation", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	var req createInvitationRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if req.Email == "" {
		replyError(w, http.StatusBadRequest, "email is required")
		return
	}
	if req.Role == "" {
		req.Role = "MEMBER"
	}

	// V-03: Validate role against whitelist and prevent escalation
	validInviteRoles := map[string]bool{"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}
	if !validInviteRoles[req.Role] {
		replyError(w, http.StatusBadRequest, "role must be ADMIN, MANAGER, MEMBER, or VIEWER")
		return
	}
	if req.Role == "ADMIN" && role != "OWNER" {
		replyError(w, http.StatusForbidden, "Only workspace owner can invite with ADMIN role")
		return
	}

	var existingMemberID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT wm.id FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ? AND u.email = ?
	`, workspaceID, req.Email).Scan(&existingMemberID)
	if err == nil {
		replyError(w, http.StatusConflict, "User is already a member of this workspace")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing member by email", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var existingInviteID string
	err = h.db.QueryRowContext(r.Context(), `
		SELECT id FROM workspace_invitations
		WHERE workspace_id = ? AND email = ? AND accepted_at IS NULL AND expires_at > datetime('now')
	`, workspaceID, req.Email).Scan(&existingInviteID)
	if err == nil {
		replyError(w, http.StatusConflict, "An active invitation already exists for this email")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing invitation", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(7 * 24 * time.Hour)
	invID := generateCUID()
	token, err := generateToken()
	if err != nil {
		h.logger.Error("generate invitation token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO workspace_invitations (id, workspace_id, email, role, invited_by, token, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		invID, workspaceID, req.Email, req.Role, user.ID, token,
		expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		h.logger.Error("insert invitation", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, invitationResponse{
		ID:          invID,
		WorkspaceID: workspaceID,
		Email:       req.Email,
		Role:        req.Role,
		InvitedBy:   user.ID,
		Token:       token,
		ExpiresAt:   expiresAt.Format(time.RFC3339),
		CreatedAt:   now.Format(time.RFC3339),
		Inviter: &inviterUser{
			ID:       user.ID,
			Email:    user.Email,
			FullName: &user.Name,
		},
	})
}
