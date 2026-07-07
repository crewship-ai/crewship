package api

// Self-service profile endpoints (#867.1). Every authenticated user can
// edit their own identity fields and change their own password without
// needing any workspace role. Avatar upload is tracked separately (needs
// a StorageProvider + authed serve endpoint) and is not wired here.

import (
	"database/sql"
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
}

// NewUserProfileHandler wires the profile handler. sessions may be nil
// (password change still succeeds; it just cannot revoke sibling
// sessions).
func NewUserProfileHandler(db *sql.DB, logger *slog.Logger, store sessions.Store) *UserProfileHandler {
	return &UserProfileHandler{db: db, logger: logger, sessions: store}
}

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
		h.logger.Error("update profile", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		h.logger.Error("load profile", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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

	var hashed sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		"SELECT hashed_password FROM users WHERE id = ?", user.ID).Scan(&hashed)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if err != nil {
		h.logger.Error("load password hash", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		h.logger.Error("hash new password", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE users SET hashed_password = ?, updated_at = ? WHERE id = ?",
		string(newHash), now, user.ID); err != nil {
		h.logger.Error("update password", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	revoked := h.revokeOtherSessions(r, user.ID, user.SessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":          true,
		"sessions_revoked": revoked,
	})
}

// revokeOtherSessions invalidates every active session for the user
// except keepSid (the caller's current session). When keepSid is empty
// (CLI-token auth — no user_sessions row) all sessions are revoked.
func (h *UserProfileHandler) revokeOtherSessions(r *http.Request, userID, keepSid string) int {
	if h.sessions == nil {
		return 0
	}
	ctx := r.Context()
	if keepSid == "" {
		n, err := h.sessions.RevokeAllForUser(ctx, userID, sessions.ReasonPasswordChange)
		if err != nil {
			h.logger.Error("revoke all sessions on password change", "error", err)
			return 0
		}
		return int(n)
	}
	list, err := h.sessions.ListActiveForUser(ctx, userID)
	if err != nil {
		h.logger.Error("list sessions on password change", "error", err)
		return 0
	}
	count := 0
	for _, s := range list {
		if s.ID == keepSid {
			continue
		}
		if err := h.sessions.Revoke(ctx, s.ID, sessions.ReasonPasswordChange); err != nil {
			h.logger.Warn("revoke sibling session", "session_id", s.ID, "error", err)
			continue
		}
		count++
	}
	return count
}
