package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Provisioning a workspace member.
//
// The invite flow it replaces could not work on any instance without a mail
// server: CreateInvitation wrote a workspace_invitations row, never called
// the mailer (it holds none), and the token it returned never reached the
// UI. The invitee was told nothing and the admin had no link to pass on.
//
// One action instead: create the account if the email is new, put the person
// in the workspace, and hand the admin a setup link to deliver however they
// like — Slack, SMS, in person. When a mailer is eventually wired the same
// endpoint can also send it; the link stays as the fallback that always
// works.
//
// No password is chosen here, by anyone. The invitee sets their own through
// the reset-password page that already ships, which is why this needs no
// force-change-on-first-login machinery: there is nothing to force a change
// away from. An admin-chosen password would also live forever in whatever
// chat window it was pasted into.

// accountSetupTTL is how long a setup link stays valid.
//
// Deliberately far longer than resetTokenTTL (30 min). A reset is requested
// by someone sitting at the screen waiting for it; a setup link is sent by a
// third party who may write the message on Friday and be read on Monday. The
// two share a table and a redemption path but not a lifetime, which is what
// the separate `account_setup` purpose is for — a /forgot must not delete a
// pending setup token, and this must not loosen the reset window.
const accountSetupTTL = 7 * 24 * time.Hour

type provisionRequest struct {
	Email    string `json:"email"`
	Role     string `json:"role"`
	FullName string `json:"full_name"`
}

type provisionResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	// CreatedUser distinguishes "a new account exists" from "an existing
	// person joined this workspace". The UI says different things for each,
	// and only the first genuinely needs the setup link.
	CreatedUser bool   `json:"created_user"`
	SetupURL    string `json:"setup_url"`
	ExpiresAt   string `json:"expires_at"`
}

// provisionableRoles mirrors the roles the membership API already accepts.
// OWNER is absent on purpose: transferring ownership is its own operation
// with its own consequences, not something to do while adding a colleague.
var provisionableRoles = map[string]bool{
	"ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true,
}

// ProvisionMember creates (or reuses) an account and adds it to the
// workspace, returning a one-time setup link.
// POST /api/v1/workspaces/{workspaceId}/members/provision
func (h *WorkspaceHandler) ProvisionMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		// Same tier as POST /members. A MANAGER may change someone's role
		// but must not mint accounts.
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var req provisionRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		replyError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	role := strings.ToUpper(strings.TrimSpace(req.Role))
	if role == "" {
		role = "MEMBER"
	}
	if !provisionableRoles[role] {
		replyError(w, http.StatusBadRequest, "role must be one of ADMIN, MANAGER, MEMBER, VIEWER")
		return
	}

	base, err := publicBaseURL()
	if err != nil {
		// Without a public URL there is no link to hand over, and a
		// provisioned account nobody can reach is worse than a refusal.
		replyError(w, http.StatusServiceUnavailable,
			"CREWSHIP_PUBLIC_URL is not configured, so no setup link can be produced")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "provision: begin tx", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	var userID string
	createdUser := false
	err = tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE email = ?`, email).Scan(&userID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		userID = uuid.NewString()
		createdAt := time.Now().UTC().Format(time.RFC3339)
		// hashed_password stays NULL: the invitee sets it via the setup
		// link. email_verified stays NULL too — we cannot verify an address
		// we never mailed, and claiming otherwise would be a lie in the
		// audit trail.
		if _, err := tx.ExecContext(r.Context(),
			`INSERT INTO users (id, email, full_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			userID, email, strings.TrimSpace(req.FullName), createdAt, createdAt); err != nil {
			replyInternalError(w, h.logger, "provision: insert user", err)
			return
		}
		createdUser = true
	case err != nil:
		replyInternalError(w, h.logger, "provision: lookup user", err)
		return
	}

	var already int
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		workspaceID, userID).Scan(&already); err != nil {
		replyInternalError(w, h.logger, "provision: membership check", err)
		return
	}
	if already > 0 {
		replyError(w, http.StatusConflict, "that person is already a member of this workspace")
		return
	}

	memberID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		memberID, workspaceID, userID, role, now, now); err != nil {
		replyInternalError(w, h.logger, "provision: insert membership", err)
		return
	}

	rawToken, err := generateResetToken()
	if err != nil {
		replyInternalError(w, h.logger, "provision: token gen", err)
		return
	}
	expires := time.Now().UTC().Add(accountSetupTTL)
	// Replace any earlier setup token for this address so re-issuing a link
	// invalidates the one that went astray — the same handoff contract
	// /forgot has, scoped to this purpose so the two never clobber each
	// other.
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM verification_tokens WHERE identifier = ? AND purpose = 'account_setup'`, email); err != nil {
		replyInternalError(w, h.logger, "provision: clear old setup token", err)
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO verification_tokens (identifier, token, expires, purpose)
		 VALUES (?, ?, ?, 'account_setup')`,
		email, hashResetToken(rawToken), expires.Format(time.RFC3339)); err != nil {
		replyInternalError(w, h.logger, "provision: store setup token", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "provision: commit", err)
		return
	}

	// Named actor, because minting an account for someone else is exactly
	// the kind of thing an audit reader needs attributed.
	h.logger.Info("workspace member provisioned",
		"workspace_id", workspaceID, "email", email, "role", role,
		"created_user", createdUser, "by_user_id", UserFromContext(r.Context()).ID)

	u := *base
	u.Path = "/reset-password"
	q := u.Query()
	q.Set("token", rawToken)
	u.RawQuery = q.Encode()

	writeJSON(w, http.StatusCreated, provisionResponse{
		UserID:      userID,
		Email:       email,
		Role:        role,
		CreatedUser: createdUser,
		SetupURL:    u.String(),
		ExpiresAt:   expires.Format(time.RFC3339),
	})
}

// publicBaseURL parses CREWSHIP_PUBLIC_URL, the same origin the recovery
// handler builds reset links from. Kept as a function rather than reusing
// RecoveryHandler.publicBase so provisioning does not have to hold a
// reference to an unrelated handler.
func publicBaseURL() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("CREWSHIP_PUBLIC_URL"))
	if raw == "" {
		return nil, errors.New("CREWSHIP_PUBLIC_URL not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("CREWSHIP_PUBLIC_URL is not an absolute URL")
	}
	return u, nil
}
