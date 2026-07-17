package api

// Self-service profile endpoints (#867.1). Every authenticated user can
// edit their own identity fields, change their own password, and upload
// their own avatar (users_avatar.go, #889) without needing any workspace
// role.

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost matches the signup/bootstrap/recovery paths (cost 12).
const profileBcryptCost = 12

// UserProfileHandler serves /api/v1/users/me — the caller's own account.
type UserProfileHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	sessions sessions.Store
	// avatarRoot is the storage base for uploaded avatars (the router's
	// storagePath). Empty when storage isn't configured — avatar upload/serve
	// then fail closed rather than writing to an unintended location. Set via
	// SetAvatarRoot, mirroring MissionHandler.SetStoragePath.
	avatarRoot string
}

// NewUserProfileHandler wires the profile handler. sessions may be nil
// (password change still succeeds; it just cannot revoke sibling
// sessions).
func NewUserProfileHandler(db *sql.DB, logger *slog.Logger, store sessions.Store) *UserProfileHandler {
	return &UserProfileHandler{db: db, logger: logger, sessions: store}
}

// SetAvatarRoot points avatar storage at the given base path (the router's
// storagePath). Avatars land under <root>/avatars/<userID>.
func (h *UserProfileHandler) SetAvatarRoot(root string) { h.avatarRoot = root }

type userProfileResponse struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	FullName  *string `json:"full_name"`
	AvatarURL *string `json:"avatar_url"`
}

type updateProfileRequest struct {
	FullName *string `json:"full_name"`
}

// UpdateProfile updates the caller's own mutable identity fields.
// PATCH /api/v1/users/me
//
// Currently only full_name is editable. Email changes require a
// re-verification flow and are intentionally out of scope — an `email`
// field in the body is ignored.
func (h *UserProfileHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req updateProfileRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.FullName == nil {
		replyError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	name := strings.TrimSpace(*req.FullName)
	if len(name) < 1 || len(name) > 100 {
		replyError(w, http.StatusBadRequest, "full_name must be 1-100 characters")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET full_name = ?, updated_at = ? WHERE id = ?", name, now, user.ID); err != nil {
		replyInternalError(w, h.logger, "update profile", err)
		return
	}

	h.writeProfile(w, r, user.ID)
}

func (h *UserProfileHandler) writeProfile(w http.ResponseWriter, r *http.Request, userID string) {
	var resp userProfileResponse
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id, email, full_name, avatar_url FROM users WHERE id = ?", userID).
		Scan(&resp.ID, &resp.Email, &resp.FullName, &resp.AvatarURL)
	if err != nil {
		replyInternalError(w, h.logger, "load profile", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword rotates the caller's password.
// POST /api/v1/users/me/password
//
// Verifies the current password, stores a fresh bcrypt hash, then
// revokes every OTHER active session for the user (reason
// "password_change") — the caller's current session is preserved so they
// are not logged out of the tab they just used. CLI-token callers have no
// current browser session, so all browser sessions are revoked.
func (h *UserProfileHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var req changePasswordRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if len(req.NewPassword) < 8 {
		replyError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	// bcrypt silently truncates — and GenerateFromPassword errors —
	// past 72 bytes, so reject over-long input as a 400 rather than
	// letting it become a 500.
	if len(req.NewPassword) > 72 {
		replyError(w, http.StatusBadRequest, "new password must be at most 72 bytes")
		return
	}

	var hashed sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT hashed_password FROM users WHERE id = ?", user.ID).Scan(&hashed)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load password hash", err)
		return
	}
	if !hashed.Valid || hashed.String == "" {
		// OAuth-only accounts have no password to verify against.
		replyError(w, http.StatusBadRequest,
			"no password set for this account — use password recovery to set one")
		return
	}
	if bcryptCompareHashAndPassword(hashed.String, req.CurrentPassword) != nil {
		replyError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), profileBcryptCost)
	if err != nil {
		replyInternalError(w, h.logger, "hash new password", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET hashed_password = ?, updated_at = ? WHERE id = ?",
		string(newHash), now, user.ID); err != nil {
		replyInternalError(w, h.logger, "update password", err)
		return
	}

	// Revoking sibling sessions is part of the password-rotation security
	// contract — if it fails we must NOT report success, or the caller
	// believes their old sessions are dead when they may still be live.
	revoked, err := h.revokeOtherSessions(r, user.ID, user.SessionID)
	if err != nil {
		replyInternalError(w, h.logger, "revoke sessions on password change", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"sessions_revoked": revoked,
	})
}

// revokeOtherSessions invalidates every active session for the user
// except keepSid (the caller's current session). When keepSid is empty
// (CLI-token auth — no user_sessions row) all sessions are revoked. A
// non-nil error means the security guarantee could not be met and the
// caller must fail the request.
func (h *UserProfileHandler) revokeOtherSessions(r *http.Request, userID, keepSid string) (int, error) {
	if h.sessions == nil {
		return 0, nil
	}
	ctx := r.Context()
	if keepSid == "" {
		n, err := h.sessions.RevokeAllForUser(ctx, userID, sessions.ReasonPasswordChange)
		if err != nil {
			return 0, fmt.Errorf("revoke all sessions: %w", err)
		}
		return int(n), nil
	}
	list, err := h.sessions.ListActiveForUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("list active sessions: %w", err)
	}
	count := 0
	for _, s := range list {
		if s.ID == keepSid {
			continue
		}
		if err := h.sessions.Revoke(ctx, s.ID, sessions.ReasonPasswordChange); err != nil {
			return count, fmt.Errorf("revoke sibling session %s: %w", s.ID, err)
		}
		count++
	}
	return count, nil
}
