package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
	wshub "github.com/crewship-ai/crewship/internal/ws"
)

// InstanceAdminHandler is Admin › People & workspaces: what an instance
// administrator can do across every workspace on the server — add a person,
// give or take access anywhere, create, hand over and delete workspaces,
// suspend an account, reissue a setup link, and name the other instance
// admins. Every route sits behind authedInstance (instance_admin.go), with no
// workspace in the request: the admin need not belong to what they manage.
//
// Every write lands in instance_audit_logs, which outlives the workspace or
// account it names.
type InstanceAdminHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	sessions sessions.Store
	journal  journal.Emitter
	hub      *wshub.Hub
}

// NewInstanceAdminHandler wires the instance admin actions. The session store
// must be the one the auth middleware checks, or a suspension would revoke
// rows nothing reads.
func NewInstanceAdminHandler(db *sql.DB, logger *slog.Logger, store sessions.Store, j journal.Emitter, hub *wshub.Hub) *InstanceAdminHandler {
	return &InstanceAdminHandler{db: db, logger: logger, sessions: store, journal: j, hub: hub}
}

// instanceRoles are the workspace roles an instance admin may grant. OWNER is
// among them: minting the first owner of a workspace is exactly what the
// workspace ladder cannot do and this surface exists for.
var instanceRoles = map[string]bool{"OWNER": true, "ADMIN": true, "MANAGER": true, "MEMBER": true, "VIEWER": true}

func normRole(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// ── People ──────────────────────────────────────────────────────────────────

type instanceMembershipInput struct {
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
}

type createPersonRequest struct {
	Email       string                    `json:"email"`
	FullName    string                    `json:"full_name"`
	Memberships []instanceMembershipInput `json:"memberships"`
}

type createPersonResponse struct {
	UserID      string                    `json:"user_id"`
	Email       string                    `json:"email"`
	Memberships []instanceMembershipInput `json:"memberships"`
	SetupURL    string                    `json:"setup_url"`
	ExpiresAt   string                    `json:"expires_at"`
}

// CreatePerson creates an account, puts it in any number of workspaces and
// returns a setup link for the admin to pass on. No email is sent and no
// password is chosen by anyone but the person.
// POST /api/v1/admin/instance/people
func (h *InstanceAdminHandler) CreatePerson(w http.ResponseWriter, r *http.Request) {
	var req createPersonRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		replyError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	seen := map[string]bool{}
	for i, m := range req.Memberships {
		req.Memberships[i].Role = normRole(m.Role)
		if !instanceRoles[req.Memberships[i].Role] {
			replyError(w, http.StatusBadRequest, "role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER")
			return
		}
		if seen[m.WorkspaceID] {
			replyError(w, http.StatusBadRequest, "a workspace may appear only once")
			return
		}
		seen[m.WorkspaceID] = true
	}
	origin := InstanceURLFromRequest(r, strings.TrimSpace(os.Getenv("CREWSHIP_PUBLIC_URL")))
	if origin == "" {
		replyError(w, http.StatusServiceUnavailable, "cannot determine this instance's URL; set CREWSHIP_PUBLIC_URL")
		return
	}

	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "create person: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	var existing string
	switch err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE email = ?`, email).Scan(&existing); {
	case err == nil:
		// Adding someone who already has an account is a membership change,
		// made from their profile — never a second account or a setup link
		// for an account somebody may already control.
		replyError(w, http.StatusConflict, "an account with this email already exists; add it to a workspace instead")
		return
	case !errors.Is(err, sql.ErrNoRows):
		replyInternalError(w, h.logger, "create person: lookup", err)
		return
	}
	for _, m := range req.Memberships {
		if ok, err := liveWorkspaceTx(ctx, tx, m.WorkspaceID); err != nil {
			replyInternalError(w, h.logger, "create person: workspace", err)
			return
		} else if !ok {
			replyError(w, http.StatusNotFound, "workspace not found: "+m.WorkspaceID)
			return
		}
	}

	userID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	var fullName any
	if n := strings.TrimSpace(req.FullName); n != "" {
		fullName = n
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, email, full_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		userID, email, fullName, now, now); err != nil {
		replyInternalError(w, h.logger, "create person: insert", err)
		return
	}
	for _, m := range req.Memberships {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), m.WorkspaceID, userID, m.Role, now, now); err != nil {
			replyInternalError(w, h.logger, "create person: membership", err)
			return
		}
	}
	raw, expires, err := issueSetupTokenTx(ctx, tx, email)
	if err != nil {
		replyInternalError(w, h.logger, "create person: setup token", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "create person: commit", err)
		return
	}

	auditInstance(r, h.db, "instance.user_created", "user", userID, "", map[string]any{
		"email": email, "memberships": req.Memberships,
	})
	// One row per workspace too, so each workspace's trail shows who joined
	// it and how, not only the person's.
	for _, m := range req.Memberships {
		auditInstance(r, h.db, "instance.member_added", "workspace_member", userID, m.WorkspaceID, map[string]any{"role": m.Role, "via": "user_created"})
	}
	if req.Memberships == nil {
		req.Memberships = []instanceMembershipInput{}
	}
	writeJSON(w, http.StatusCreated, createPersonResponse{
		UserID: userID, Email: email, Memberships: req.Memberships,
		SetupURL: setupLinkURL(origin, raw), ExpiresAt: expires.Format(time.RFC3339),
	})
}

// person loads the {userId} a person action is about, or answers 404.
func (h *InstanceAdminHandler) person(w http.ResponseWriter, r *http.Request) (id, email string, ok bool) {
	id = strings.TrimSpace(r.PathValue("userId"))
	err := h.db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id = ?`, id).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) || id == "" {
		replyError(w, http.StatusNotFound, "user not found")
		return "", "", false
	}
	if err != nil {
		replyInternalError(w, h.logger, "instance admin: user lookup", err)
		return "", "", false
	}
	return id, email, true
}

type suspendRequest struct {
	Reason string `json:"reason"`
}

// Suspend blocks an account: no sign-in, no token refresh, no CLI token, and
// every open session ends now. Memberships stay, so Reactivate restores
// exactly what the person had.
// POST /api/v1/admin/instance/people/{userId}/suspend
func (h *InstanceAdminHandler) Suspend(w http.ResponseWriter, r *http.Request) {
	id, email, ok := h.person(w, r)
	if !ok {
		return
	}
	var req suspendRequest
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			replyError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
	}
	if actor := UserFromContext(r.Context()); actor != nil && actor.ID == id {
		replyError(w, http.StatusConflict, "you cannot suspend your own account")
		return
	}
	if backup.IsInstanceOwner(email) {
		replyError(w, http.StatusConflict, "this account is the instance owner (CREWSHIP_OWNER_EMAIL) and cannot be suspended")
		return
	}
	ctx := r.Context()
	now := time.Now().UTC().Format(time.RFC3339)
	var reason any
	if s := strings.TrimSpace(req.Reason); s != "" {
		reason = s
	}
	if _, err := h.db.ExecContext(ctx,
		`UPDATE users SET suspended_at = ?, suspended_reason = ? WHERE id = ?`, now, reason, id); err != nil {
		replyInternalError(w, h.logger, "suspend", err)
		return
	}
	var revoked int64
	if h.sessions != nil {
		n, err := h.sessions.RevokeAllForUser(ctx, id, reasonAdminRevoke)
		if err != nil {
			replyInternalError(w, h.logger, "suspend: revoke sessions", err)
			return
		}
		revoked = n
	}
	res, err := h.db.ExecContext(ctx,
		`UPDATE cli_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		replyInternalError(w, h.logger, "suspend: revoke CLI tokens", err)
		return
	}
	tokens, _ := res.RowsAffected()
	auditInstance(r, h.db, "instance.user_suspended", "user", id, "", map[string]any{
		"reason": req.Reason, "sessions_revoked": revoked, "cli_tokens_revoked": tokens,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": id, "suspended_at": now, "sessions_revoked": revoked, "cli_tokens_revoked": tokens,
	})
}

// Reactivate lifts a suspension. Sessions and CLI tokens ended by it stay
// ended; the person signs in again.
// POST /api/v1/admin/instance/people/{userId}/reactivate
func (h *InstanceAdminHandler) Reactivate(w http.ResponseWriter, r *http.Request) {
	id, _, ok := h.person(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET suspended_at = NULL, suspended_reason = NULL WHERE id = ?`, id); err != nil {
		replyInternalError(w, h.logger, "reactivate", err)
		return
	}
	auditInstance(r, h.db, "instance.user_reactivated", "user", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// IssueSetupLink mints a fresh setup link for an account nobody controls yet,
// replacing any earlier one. The plaintext is never stored, which is why the
// "copy link" in the UI is really "issue a new link".
// POST /api/v1/admin/instance/people/{userId}/setup-link
func (h *InstanceAdminHandler) IssueSetupLink(w http.ResponseWriter, r *http.Request) {
	id, email, ok := h.person(w, r)
	if !ok {
		return
	}
	origin := InstanceURLFromRequest(r, strings.TrimSpace(os.Getenv("CREWSHIP_PUBLIC_URL")))
	if origin == "" {
		replyError(w, http.StatusServiceUnavailable, "cannot determine this instance's URL; set CREWSHIP_PUBLIC_URL")
		return
	}
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "setup link: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	claimed, err := accountClaimedTx(ctx, tx, id)
	if err != nil {
		replyInternalError(w, h.logger, "setup link: claimed check", err)
		return
	}
	if claimed {
		// See accountClaimedTx: a setup token for a controlled account is a
		// takeover. The person resets their own password instead.
		replyError(w, http.StatusConflict, "this person already signs in; they can reset their own password")
		return
	}
	raw, expires, err := issueSetupTokenTx(ctx, tx, email)
	if err != nil {
		replyInternalError(w, h.logger, "setup link: token", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "setup link: commit", err)
		return
	}
	auditInstance(r, h.db, "instance.setup_link_issued", "user", id, "", nil)
	writeJSON(w, http.StatusOK, map[string]string{
		"user_id": id, "setup_url": setupLinkURL(origin, raw), "expires_at": expires.Format(time.RFC3339),
	})
}

// RevokeSetupLink voids a pending setup link, e.g. one sent to the wrong
// place. 404 when there is none.
// DELETE /api/v1/admin/instance/people/{userId}/setup-link
func (h *InstanceAdminHandler) RevokeSetupLink(w http.ResponseWriter, r *http.Request) {
	id, email, ok := h.person(w, r)
	if !ok {
		return
	}
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM verification_tokens WHERE identifier = ? AND purpose = 'account_setup'`, email)
	if err != nil {
		replyInternalError(w, h.logger, "revoke setup link", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "no pending setup link")
		return
	}
	auditInstance(r, h.db, "instance.setup_link_revoked", "user", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Instance admins ─────────────────────────────────────────────────────────

// GrantAdmin names a person an instance administrator.
// PUT /api/v1/admin/instance/admins/{userId}
func (h *InstanceAdminHandler) GrantAdmin(w http.ResponseWriter, r *http.Request) {
	id, _, ok := h.person(w, r)
	if !ok {
		return
	}
	var suspended sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT suspended_at FROM users WHERE id = ?`, id).Scan(&suspended); err != nil {
		replyInternalError(w, h.logger, "grant admin: lookup", err)
		return
	}
	if suspended.Valid && suspended.String != "" {
		replyError(w, http.StatusConflict, "reactivate this account before making it an instance admin")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET instance_role = 'ADMIN' WHERE id = ?`, id); err != nil {
		replyInternalError(w, h.logger, "grant admin", err)
		return
	}
	auditInstance(r, h.db, "instance.admin_granted", "user", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// RevokeAdmin removes a named instance administrator. Nobody removes
// themselves (another admin does, so there is always one who saw it), and
// the env owner is not in this list to remove.
// DELETE /api/v1/admin/instance/admins/{userId}
func (h *InstanceAdminHandler) RevokeAdmin(w http.ResponseWriter, r *http.Request) {
	id, email, ok := h.person(w, r)
	if !ok {
		return
	}
	if actor := UserFromContext(r.Context()); actor != nil && actor.ID == id {
		replyError(w, http.StatusConflict, "another instance admin has to remove you")
		return
	}
	if backup.IsInstanceOwner(email) {
		replyError(w, http.StatusConflict, "this account is the instance owner (CREWSHIP_OWNER_EMAIL); change the server's environment instead")
		return
	}
	res, err := h.db.ExecContext(r.Context(), `UPDATE users SET instance_role = NULL WHERE id = ? AND instance_role IS NOT NULL`, id)
	if err != nil {
		replyInternalError(w, h.logger, "revoke admin", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		replyError(w, http.StatusNotFound, "this person is not a named instance admin")
		return
	}
	auditInstance(r, h.db, "instance.admin_revoked", "user", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// ── Memberships ─────────────────────────────────────────────────────────────

type setMembershipRequest struct {
	Role string `json:"role"`
}

// SetMembership gives a person access to a workspace, or changes their role
// there. Unlike the workspace route, the ladder does not apply: an instance
// admin may make someone OWNER. The last OWNER cannot be demoted — hand the
// workspace over first.
// PUT /api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}
func (h *InstanceAdminHandler) SetMembership(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(r.PathValue("workspaceId"))
	userID, _, ok := h.person(w, r)
	if !ok {
		return
	}
	var req setMembershipRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	role := normRole(req.Role)
	if !instanceRoles[role] {
		replyError(w, http.StatusBadRequest, "role must be one of OWNER, ADMIN, MANAGER, MEMBER, VIEWER")
		return
	}
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "set membership: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if live, err := liveWorkspaceTx(ctx, tx, wsID); err != nil {
		replyInternalError(w, h.logger, "set membership: workspace", err)
		return
	} else if !live {
		replyError(w, http.StatusNotFound, "workspace not found")
		return
	}
	var current string
	now := time.Now().UTC().Format(time.RFC3339)
	created := false
	switch err := tx.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID).Scan(&current); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			uuid.NewString(), wsID, userID, role, now, now); err != nil {
			replyInternalError(w, h.logger, "set membership: insert", err)
			return
		}
		created = true
	case err != nil:
		replyInternalError(w, h.logger, "set membership: lookup", err)
		return
	default:
		if current == "OWNER" && role != "OWNER" {
			if last, err := lastOwnerTx(ctx, tx, wsID, userID); err != nil {
				replyInternalError(w, h.logger, "set membership: owner count", err)
				return
			} else if last {
				replyError(w, http.StatusConflict, "this is the workspace's only owner; transfer ownership first")
				return
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE workspace_members SET role = ?, updated_at = ? WHERE workspace_id = ? AND user_id = ?`,
			role, now, wsID, userID); err != nil {
			replyInternalError(w, h.logger, "set membership: update", err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "set membership: commit", err)
		return
	}
	action := "instance.member_role_changed"
	if created {
		action = "instance.member_added"
	}
	auditInstance(r, h.db, action, "workspace_member", userID, wsID, map[string]any{"role": role, "previous_role": current})
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"workspace_id": wsID, "user_id": userID, "role": role, "created": created})
}

// RemoveMembership takes a person out of a workspace. Their pages move first,
// exactly as when a workspace admin removes them; the last OWNER stays.
// DELETE /api/v1/admin/instance/workspaces/{workspaceId}/members/{userId}
func (h *InstanceAdminHandler) RemoveMembership(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(r.PathValue("workspaceId"))
	userID := strings.TrimSpace(r.PathValue("userId"))
	ctx := r.Context()
	var role string
	err := h.db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "not a member of this workspace")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "remove membership: lookup", err)
		return
	}
	if role == "OWNER" {
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			replyInternalError(w, h.logger, "remove membership: begin", err)
			return
		}
		last, err := lastOwnerTx(ctx, tx, wsID, userID)
		_ = tx.Rollback()
		if err != nil {
			replyInternalError(w, h.logger, "remove membership: owner count", err)
			return
		}
		if last {
			replyError(w, http.StatusConflict, "this is the workspace's only owner; transfer ownership first")
			return
		}
	}
	actorID := ""
	if a := UserFromContext(ctx); a != nil {
		actorID = a.ID
	}
	if err := departWorkspace(ctx, h.db, h.journal, actorID, wsID, userID); err != nil {
		var needsManual *ErrPagesNeedManualTransfer
		if errors.As(err, &needsManual) {
			replyError(w, http.StatusConflict, "cannot remove this member: "+err.Error())
			return
		}
		replyInternalError(w, h.logger, "remove membership", err)
		return
	}
	auditInstance(r, h.db, "instance.member_removed", "workspace_member", userID, wsID, map[string]any{"role": role})
	w.WriteHeader(http.StatusNoContent)
}

// ── Workspaces ──────────────────────────────────────────────────────────────

type createInstanceWorkspaceRequest struct {
	Name              string  `json:"name"`
	Slug              string  `json:"slug"`
	OwnerUserID       string  `json:"owner_user_id"`
	PreferredLanguage *string `json:"preferred_language"`
}

// CreateWorkspace creates a workspace owned by the person named — not by the
// admin, who is not added to it.
// POST /api/v1/admin/instance/workspaces
func (h *InstanceAdminHandler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createInstanceWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.TrimSpace(req.Slug)
	if len(req.Name) < 2 || len(req.Name) > 100 {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if len(req.Slug) < 2 || len(req.Slug) > 50 {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	if req.PreferredLanguage != nil && *req.PreferredLanguage != "" {
		resolved, err := resolveLanguage(*req.PreferredLanguage)
		if err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.PreferredLanguage = &resolved
	}
	ctx := r.Context()
	var suspended sql.NullString
	switch err := h.db.QueryRowContext(ctx, `SELECT suspended_at FROM users WHERE id = ?`, req.OwnerUserID).Scan(&suspended); {
	case errors.Is(err, sql.ErrNoRows):
		replyError(w, http.StatusBadRequest, "owner_user_id must name an existing account")
		return
	case err != nil:
		replyInternalError(w, h.logger, "create workspace: owner", err)
		return
	case suspended.Valid && suspended.String != "":
		replyError(w, http.StatusConflict, "the chosen owner is suspended")
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "create workspace: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	var taken string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE slug = ?`, req.Slug).Scan(&taken); err == nil {
		replyError(w, http.StatusConflict, "Workspace slug already taken")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		replyInternalError(w, h.logger, "create workspace: slug", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	wsID := generateCUID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug, preferred_language, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		wsID, req.Name, req.Slug, req.PreferredLanguage, now, now); err != nil {
		replyInternalError(w, h.logger, "create workspace: insert", err)
		return
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, 'OWNER', ?, ?)`,
		generateCUID(), wsID, req.OwnerUserID, now, now); err != nil {
		replyInternalError(w, h.logger, "create workspace: owner membership", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "create workspace: commit", err)
		return
	}
	auditInstance(r, h.db, "instance.workspace_created", "workspace", wsID, wsID, map[string]any{
		"name": req.Name, "slug": req.Slug, "owner_user_id": req.OwnerUserID,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": wsID, "name": req.Name, "slug": req.Slug, "owner_user_id": req.OwnerUserID, "created_at": now,
	})
}

type transferOwnershipRequest struct {
	UserID string `json:"user_id"`
}

// TransferOwnership makes a member the workspace's owner; every previous
// owner becomes an ADMIN, so the workspace keeps exactly one.
// POST /api/v1/admin/instance/workspaces/{workspaceId}/transfer-ownership
func (h *InstanceAdminHandler) TransferOwnership(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(r.PathValue("workspaceId"))
	var req transferOwnershipRequest
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.UserID) == "" {
		replyError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "transfer: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if live, err := liveWorkspaceTx(ctx, tx, wsID); err != nil {
		replyInternalError(w, h.logger, "transfer: workspace", err)
		return
	} else if !live {
		replyError(w, http.StatusNotFound, "workspace not found")
		return
	}
	var role string
	switch err := tx.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, req.UserID).Scan(&role); {
	case errors.Is(err, sql.ErrNoRows):
		replyError(w, http.StatusConflict, "the new owner must already be a member of the workspace")
		return
	case err != nil:
		replyInternalError(w, h.logger, "transfer: member", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var previous []string
	rows, err := tx.QueryContext(ctx,
		`SELECT user_id FROM workspace_members WHERE workspace_id = ? AND role = 'OWNER' AND user_id != ?`, wsID, req.UserID)
	if err != nil {
		replyInternalError(w, h.logger, "transfer: owners", err)
		return
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			replyInternalError(w, h.logger, "transfer: scan owner", err)
			return
		}
		previous = append(previous, id)
	}
	rows.Close()
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_members SET role = 'ADMIN', updated_at = ? WHERE workspace_id = ? AND role = 'OWNER' AND user_id != ?`,
		now, wsID, req.UserID); err != nil {
		replyInternalError(w, h.logger, "transfer: demote", err)
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workspace_members SET role = 'OWNER', updated_at = ? WHERE workspace_id = ? AND user_id = ?`,
		now, wsID, req.UserID); err != nil {
		replyInternalError(w, h.logger, "transfer: promote", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "transfer: commit", err)
		return
	}
	if previous == nil {
		previous = []string{}
	}
	auditInstance(r, h.db, "instance.workspace_ownership_transferred", "workspace", wsID, wsID, map[string]any{
		"new_owner_user_id": req.UserID, "previous_owner_user_ids": previous,
	})
	writeJSON(w, http.StatusOK, map[string]any{"workspace_id": wsID, "owner_user_id": req.UserID, "previous_owner_user_ids": previous})
}

// DeleteWorkspace deletes any workspace, confirmed by typing its slug. The
// workspace route's "not your only workspace" guard is a self-service rule;
// an instance admin deletes on someone else's behalf.
// DELETE /api/v1/admin/instance/workspaces/{workspaceId}
func (h *InstanceAdminHandler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(r.PathValue("workspaceId"))
	var req deleteWorkspaceRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	ctx := r.Context()
	var slug, name string
	err := h.db.QueryRowContext(ctx, `SELECT slug, name FROM workspaces WHERE id = ? AND deleted_at IS NULL`, wsID).Scan(&slug, &name)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "workspace not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "delete workspace: lookup", err)
		return
	}
	if req.ConfirmSlug != slug {
		replyError(w, http.StatusBadRequest, "confirm_slug does not match the workspace slug")
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		replyInternalError(w, h.logger, "delete workspace: begin", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck
	crewIDs, err := softDeleteWorkspaceTx(ctx, tx, wsID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		replyInternalError(w, h.logger, "delete workspace", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "delete workspace: commit", err)
		return
	}
	for _, cid := range crewIDs {
		broadcastWorkspaceEvent(h.hub, wsID, "crew.deleted", map[string]string{"id": cid})
	}
	broadcastWorkspaceEvent(h.hub, wsID, "workspace.deleted", map[string]string{"id": wsID, "slug": slug})
	auditInstance(r, h.db, "instance.workspace_deleted", "workspace", wsID, wsID, map[string]any{
		"name": name, "slug": slug, "crews": len(crewIDs),
	})
	w.WriteHeader(http.StatusNoContent)
}

// ── Audit ───────────────────────────────────────────────────────────────────

type instanceAuditRow struct {
	ID                string  `json:"id"`
	UserID            *string `json:"user_id"`
	UserEmail         *string `json:"user_email"`
	Action            string  `json:"action"`
	EntityType        string  `json:"entity_type"`
	EntityID          *string `json:"entity_id"`
	TargetWorkspaceID *string `json:"target_workspace_id"`
	Metadata          string  `json:"metadata"`
	CreatedAt         string  `json:"created_at"`
}

// AuditLog lists instance-level actions, newest first. ?limit= (default 100,
// max 500) and ?entity_id= narrow it to one person or workspace.
// GET /api/v1/admin/instance/audit
func (h *InstanceAdminHandler) AuditLog(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 500)
	}
	q := `SELECT a.id, a.user_id, u.email, a.action, a.entity_type, a.entity_id, a.target_workspace_id, COALESCE(a.metadata, '{}'), a.created_at
		FROM instance_audit_logs a LEFT JOIN users u ON u.id = a.user_id`
	var args []any
	if e := strings.TrimSpace(r.URL.Query().Get("entity_id")); e != "" {
		q += ` WHERE a.entity_id = ? OR a.target_workspace_id = ?`
		args = append(args, e, e)
	}
	q += ` ORDER BY a.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		replyInternalError(w, h.logger, "instance audit", err)
		return
	}
	defer rows.Close()
	out := []instanceAuditRow{}
	for rows.Next() {
		var a instanceAuditRow
		if err := rows.Scan(&a.ID, &a.UserID, &a.UserEmail, &a.Action, &a.EntityType, &a.EntityID, &a.TargetWorkspaceID, &a.Metadata, &a.CreatedAt); err != nil {
			replyInternalError(w, h.logger, "instance audit: scan", err)
			return
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "instance audit: rows", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── helpers ─────────────────────────────────────────────────────────────────

func liveWorkspaceTx(ctx context.Context, tx *sql.Tx, wsID string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM workspaces WHERE id = ? AND deleted_at IS NULL`, wsID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// lastOwnerTx reports whether userID is the only OWNER of the workspace.
func lastOwnerTx(ctx context.Context, tx *sql.Tx, wsID, userID string) (bool, error) {
	var others int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_members WHERE workspace_id = ? AND role = 'OWNER' AND user_id != ?`,
		wsID, userID).Scan(&others)
	return others == 0, err
}
