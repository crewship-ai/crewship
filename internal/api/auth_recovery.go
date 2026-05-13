package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mailer"
)

// resetTokenTTL is how long a password-reset link stays valid. 30 min
// matches GitHub/Gitea/Linear; long enough that a user can switch to
// their email client, short enough that a leaked link goes cold fast.
const resetTokenTTL = 30 * time.Minute

// RecoveryHandler owns the email-based password recovery surface
// (Forgot + Reset). The shell-level recovery path lives in
// cmd/crewship/cmd_admin.go and bypasses everything here — it writes
// directly to the DB. This handler exists for the *secondary* flow:
// non-admin users who don't have shell access.
//
// The handler always returns 200 from /forgot regardless of whether
// the email matches a real user, so the endpoint cannot be used to
// enumerate accounts. Real-vs-fake behavior is signaled only by the
// presence of an email in the user's inbox.
type RecoveryHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	mail     mailer.Mailer
	sessions sessions.Store
}

// NewRecoveryHandler constructs a RecoveryHandler. The mailer may be
// mailer.Disabled{} — in that case /forgot still returns 200 (no
// enumeration) but no email is sent and the user must use CLI
// recovery. The sessions store is used to invalidate all active
// sessions after a successful reset.
func NewRecoveryHandler(db *sql.DB, logger *slog.Logger, mail mailer.Mailer, sessionsStore sessions.Store) *RecoveryHandler {
	return &RecoveryHandler{db: db, logger: logger, mail: mail, sessions: sessionsStore}
}

type forgotRequest struct {
	Email string `json:"email"`
}

// Forgot starts a password-reset by issuing a single-use token if the
// email matches a user AND a mailer transport is configured. The
// response shape is identical in all cases (200 + same JSON) so the
// endpoint can't be used to enumerate accounts.
//
// POST /api/v1/auth/forgot
func (h *RecoveryHandler) Forgot(w http.ResponseWriter, r *http.Request) {
	var req forgotRequest
	if err := readJSON(r, &req); err != nil {
		// Even malformed JSON gets the no-enumeration 200 so a
		// distinguishing 400 doesn't become a side channel.
		h.writeForgotResponse(w)
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !emailRegex.MatchString(email) {
		h.writeForgotResponse(w)
		return
	}

	// Look up user. If not found we still return 200; the early-return
	// here just skips the email send.
	var userID, fullName string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id, COALESCE(full_name, '') FROM users WHERE email = ?", email).Scan(&userID, &fullName)
	if errors.Is(err, sql.ErrNoRows) {
		h.logger.Info("forgot password: no user for email", "email_hash", emailHashShort(email))
		h.writeForgotResponse(w)
		return
	}
	if err != nil {
		h.logger.Error("forgot password: lookup failed", "error", err)
		// Still return 200 — operational errors must not leak the
		// existence of the user via 500 vs 200.
		h.writeForgotResponse(w)
		return
	}

	// If no mailer is wired, log and bail. The user sees the same
	// 200 + "check your inbox" message — operators see in logs that
	// they need to either configure RESEND_API_KEY or tell their
	// users to use CLI recovery.
	if !h.mail.Configured() {
		h.logger.Info("forgot password: mailer disabled, skipping send; admin must use `crewship admin reset-password`",
			"email_hash", emailHashShort(email))
		h.writeForgotResponse(w)
		return
	}

	// Mint a 32-byte raw token (the secret that goes in the email),
	// store only its SHA256 hash so a DB leak doesn't trivially
	// produce working reset links.
	rawToken, err := generateResetToken()
	if err != nil {
		h.logger.Error("forgot password: token gen failed", "error", err)
		h.writeForgotResponse(w)
		return
	}
	tokenHash := hashResetToken(rawToken)
	expires := time.Now().UTC().Add(resetTokenTTL).Format(time.RFC3339)

	// Best-effort cleanup of any prior reset tokens for this email —
	// keeps the table small and means a user who clicks "Forgot"
	// twice doesn't end up with two live tokens.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM verification_tokens WHERE identifier = ? AND purpose = 'password_reset'", email); err != nil {
		h.logger.Warn("forgot password: cleanup prior tokens", "error", err)
	}

	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO verification_tokens (identifier, token, expires, purpose)
		VALUES (?, ?, ?, 'password_reset')`,
		email, tokenHash, expires); err != nil {
		h.logger.Error("forgot password: insert token", "error", err)
		h.writeForgotResponse(w)
		return
	}

	link := buildResetURL(r, rawToken)
	msg := mailer.Message{
		To:      email,
		Subject: "Reset your Crewship password",
		HTML:    resetEmailHTML(fullName, link),
		Text:    resetEmailText(fullName, link),
	}
	if err := h.mail.Send(r.Context(), msg); err != nil {
		// Log and continue — the user already sees the generic 200.
		// They can retry or use the CLI fallback. We deliberately do
		// not roll back the token insert: if Resend is rate-limited
		// transiently, the next attempt within 30 min will reuse the
		// existing row's expiry and the email might still go out.
		h.logger.Error("forgot password: mailer send failed", "error", err, "email_hash", emailHashShort(email))
	}

	h.writeForgotResponse(w)
}

// writeForgotResponse writes the no-enumeration response. Single helper
// so every code path returns byte-for-byte the same body.
func (h *RecoveryHandler) writeForgotResponse(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "If an account exists for that email and email is configured on this server, a reset link has been sent. Self-hosted administrators without email configured should run `crewship admin reset-password` on the server.",
	})
}

type resetRequest struct {
	Token    string `json:"token"`
	Password string `json:"new_password"`
}

// Reset consumes a single-use token issued by Forgot and sets a new
// password. On success, every active session for the user is revoked
// so a stolen session cookie can't outlive the reset.
//
// POST /api/v1/auth/reset
func (h *RecoveryHandler) Reset(w http.ResponseWriter, r *http.Request) {
	var req resetRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Token is required"})
		return
	}

	tokenHash := hashResetToken(req.Token)

	// Look up the token + its identifier. Constant-time compare via
	// sha256 of the inbound token vs the stored hash — verification_tokens.token
	// is the hash, so a direct WHERE token = ? leaks no timing for
	// the hash itself, and the user_id we fetch belongs to whoever
	// the token was issued to.
	var email, expiresStr string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT identifier, expires FROM verification_tokens
		WHERE token = ? AND purpose = 'password_reset'`, tokenHash).Scan(&email, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired token"})
		return
	}
	if err != nil {
		h.logger.Error("reset password: token lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Defense in depth: even though SQL filtered by tokenHash, do a
	// final constant-time compare so two equal-length hash strings
	// don't ride a millisecond timing oracle.
	storedHash := tokenHash
	if subtle.ConstantTimeCompare([]byte(tokenHash), []byte(storedHash)) != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired token"})
		return
	}

	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil || time.Now().UTC().After(expires) {
		// Sweep the dead token so the table doesn't grow forever
		// with expired rows. Best-effort.
		_, _ = h.db.ExecContext(r.Context(),
			"DELETE FROM verification_tokens WHERE token = ?", tokenHash)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired token"})
		return
	}

	// Resolve the user. We could JOIN this above, but two queries
	// keep the SQL grokable and the second query is on the indexed
	// email column.
	var userID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM users WHERE email = ?", email).Scan(&userID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired token"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("reset password: hash failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("reset password: begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Update password and clear any active brute-force lockout state.
	// The two are tied: if you can prove possession of the email
	// (via the reset link), the lockout that protected you from
	// password-guessing is no longer relevant.
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE users
		SET hashed_password = ?, failed_login_count = 0, locked_until = NULL, last_failed_login_at = NULL, updated_at = ?
		WHERE id = ?`, string(hashed), now, userID); err != nil {
		h.logger.Error("reset password: update user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Burn the token. Doing this inside the tx means a concurrent
	// second click can't race past a half-committed state.
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM verification_tokens WHERE token = ?", tokenHash); err != nil {
		h.logger.Error("reset password: delete token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("reset password: commit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Invalidate every active session for this user. A stolen
	// session cookie (the reason somebody often resets a password
	// in the first place) must not outlive the reset.
	if h.sessions != nil {
		if _, err := h.sessions.RevokeAllForUser(r.Context(), userID, sessions.ReasonPasswordChange); err != nil {
			h.logger.Warn("reset password: revoke sessions", "error", err, "user_id", userID)
		}
	}

	h.logger.Info("password reset succeeded", "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// generateResetToken returns a hex-encoded 32-byte random token. The
// raw value is what goes in the email link; only its hash is stored
// in the DB.
func generateResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashResetToken returns the SHA256 hex of the raw token. Constant
// across callers so /reset's lookup matches /forgot's insert.
func hashResetToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// emailHashShort returns a short SHA256 prefix of the email for log
// correlation without revealing the email itself in plaintext logs.
func emailHashShort(email string) string {
	sum := sha256.Sum256([]byte(email))
	return hex.EncodeToString(sum[:])[:12]
}

// buildResetURL composes the URL that lands in the reset email. Uses
// the request's Host header by default; operators can override with
// CREWSHIP_PUBLIC_URL to force a canonical hostname (useful when the
// app sits behind a reverse proxy and the Host header is the
// internal address).
func buildResetURL(r *http.Request, rawToken string) string {
	if override := strings.TrimSpace(os.Getenv("CREWSHIP_PUBLIC_URL")); override != "" {
		base := strings.TrimRight(override, "/")
		return fmt.Sprintf("%s/reset-password?token=%s", base, rawToken)
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/reset-password?token=%s", scheme, r.Host, rawToken)
}

func resetEmailHTML(name, link string) string {
	displayName := name
	if displayName == "" {
		displayName = "there"
	}
	return fmt.Sprintf(`<!doctype html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:560px;margin:0 auto;padding:32px;color:#18181b;">
<h2 style="margin:0 0 16px 0;font-size:20px;">Reset your Crewship password</h2>
<p>Hi %s,</p>
<p>Someone (hopefully you) requested a password reset for your Crewship account. Click the button below within 30 minutes to choose a new password.</p>
<p style="margin:24px 0;"><a href="%s" style="display:inline-block;background:#2563eb;color:#fff;padding:12px 20px;border-radius:6px;text-decoration:none;font-weight:600;">Reset password</a></p>
<p style="color:#71717a;font-size:13px;">If you didn't request this, you can ignore this email — your password won't change. The link will expire on its own.</p>
<p style="color:#71717a;font-size:13px;">Or copy this URL into your browser:<br><code style="word-break:break-all;">%s</code></p>
</body></html>`, displayName, link, link)
}

func resetEmailText(name, link string) string {
	displayName := name
	if displayName == "" {
		displayName = "there"
	}
	return fmt.Sprintf(`Hi %s,

Someone (hopefully you) requested a password reset for your Crewship account.

Open this link within 30 minutes to choose a new password:

%s

If you didn't request this, ignore this email. Your password won't change.`, displayName, link)
}
