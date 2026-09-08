package api

import (
	"database/sql"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

const revealFreshLoginWindow = 5 * time.Minute

// Freshness comes from the original server-side session creation time, not
// JWT refresh or last activity. Refreshing a stolen session must not grant a
// new reveal window. Recheck ownership/revocation here, at disclosure time.
func (h *CredentialRevealHandler) requireFreshRevealLogin(w http.ResponseWriter, r *http.Request) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	session, err := sessions.NewDBStore(h.db).Get(r.Context(), user.SessionID)
	now := time.Now().UTC()
	if err != nil || session == nil || session.UserID != user.ID || !session.Active(now) || session.CreatedAt.After(now) || now.Sub(session.CreatedAt) > revealFreshLoginWindow {
		h.denyReveal(w, user.ID, RoleFromContext(r.Context()), r.PathValue("credentialId"), "fresh_sign_in_required",
			"For your security, sign out and sign in again before revealing a credential. A fresh sign-in is valid for 5 minutes; refreshing the page does not renew it.")
		return false
	}
	var hash sql.NullString
	if err := h.db.QueryRowContext(r.Context(), "SELECT hashed_password FROM users WHERE id = ?", user.ID).Scan(&hash); err != nil {
		replyInternalError(w, h.logger, "verify reveal account security", err)
		return false
	}
	if hash.Valid && bcrypt.CompareHashAndPassword([]byte(hash.String), []byte(seedAccountPassword)) == nil {
		h.denyReveal(w, user.ID, RoleFromContext(r.Context()), r.PathValue("credentialId"), "default_password",
			"Change the default demo password in your account settings, then sign in again before revealing credentials.")
		return false
	}
	return true
}
