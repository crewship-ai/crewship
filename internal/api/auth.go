package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// AuthHandler provides user authentication endpoints including signup, login, and WebSocket token exchange.
type AuthHandler struct {
	db          *sql.DB
	logger      *slog.Logger
	validator   *auth.JWTValidator
	sessions    sessions.Store
	allowSignup bool
}

// NewAuthHandler creates an AuthHandler with the given dependencies and signup configuration.
// sessionsStore must back user_sessions (migration v63).
func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, sessionsStore sessions.Store, allowSignup bool) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator, sessions: sessionsStore, allowSignup: allowSignup}
}

// setSessionCookies creates a fresh user_sessions row and writes the
// matching access + refresh cookies. Used by signup; the credentials
// callback in nextauth.go has its own copy because it needs to share
// the cookie-name helpers with the rest of the NextAuth surface.
//
// IMPORTANT: ctx must NOT be tied to the request lifetime when called
// after a database commit. If the client disconnects between
// tx.Commit() and sessions.Create(), an r.Context() here would surface
// as context.Canceled, the caller's err-path runs cleanupOrphanedSignup,
// and the freshly-committed user/workspace gets deleted right after
// signup. Pass a background-derived context with a short timeout
// instead — the signup is already on disk, the only thing we still
// owe the client is the cookie write, and nothing useful comes from
// abandoning that work just because the TCP connection went away.
func (h *AuthHandler) setSessionCookies(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fullName, email string) error {
	if h.sessions == nil {
		return errSessionsStoreUnconfigured
	}
	sess, err := h.sessions.Create(ctx, userID, r.UserAgent(), clientIP(r), auth.RefreshTokenTTL)
	if err != nil {
		return err
	}
	access, err := h.validator.IssueAccessToken(userID, sess.ID, fullName, email)
	if err != nil {
		return err
	}
	refresh, err := h.validator.IssueRefreshToken(userID, sess.ID)
	if err != nil {
		return err
	}

	setAuthCookies(w, r, access, refresh)
	return nil
}

// errSessionsStoreUnconfigured signals that an auth path was reached
// without the sessions store wired in. Production main() always wires
// it; only test fixtures that go around NewRouter can hit this.
var errSessionsStoreUnconfigured = stringError("sessions store not configured")

type stringError string

func (e stringError) Error() string { return string(e) }

type signupRequest struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// emailSlugCleanRE replaces characters that aren't valid in workspace slugs
// with hyphens. Hoisted to package level so Signup / Bootstrap don't
// recompile the regex on every call.
var emailSlugCleanRE = regexp.MustCompile(`[^a-z0-9-]`)

// Signup registers a new user, creates their default workspace, and sets a session cookie.
// POST /api/v1/auth/signup — disabled when CREWSHIP_ALLOW_SIGNUP is false.
func (h *AuthHandler) Signup(w http.ResponseWriter, r *http.Request) {
	if !h.allowSignup {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Registration is disabled. Set CREWSHIP_ALLOW_SIGNUP=true to enable."})
		return
	}

	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if len(req.FullName) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Name must be at least 2 characters"})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid email address"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email = ?", req.Email).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Email already registered"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing email", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("hash password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slugBase := strings.Split(req.Email, "@")[0]
	slugBase = emailSlugCleanRE.ReplaceAllString(strings.ToLower(slugBase), "-")

	now := time.Now().UTC().Format(time.RFC3339)
	userID := generateCUID()
	workspaceID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("insert user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("insert membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Cookie-set must succeed for the response to honestly mean "you
	// are signed up and signed in". If it fails (validator down,
	// sessions store unreachable), tell the client and roll the
	// account back so we don't leave an orphan that can never log in
	// because of a cleanup we forgot.
	//
	// We use a fresh background context with a short timeout instead
	// of r.Context() because the user has already been committed.
	// If the client disconnects between tx.Commit and sessions.Create,
	// r.Context() goes Canceled — and we'd then delete a perfectly
	// valid user. Background-derived context decouples the post-commit
	// auth setup from the transport: the user gets created either way;
	// the cookie just doesn't reach them and they re-login normally.
	authCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.setSessionCookies(authCtx, w, r, userID, req.FullName, req.Email); err != nil {
		h.logger.Error("set session cookies after signup — rolling back", "error", err)
		h.cleanupOrphanedSignup(userID, workspaceID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to establish session — please try again"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": userID, "email": req.Email})
}

// cleanupOrphanedSignup removes the user + workspace + membership rows
// created by a Signup that committed but couldn't establish a session.
// Best-effort — we already logged the original failure, so any error
// here just means a manual sweep later. FK CASCADE on user delete
// handles workspace_members; we still nuke the workspace explicitly
// because it has no inbound FK from users.
func (h *AuthHandler) cleanupOrphanedSignup(userID, workspaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		h.logger.Error("cleanup orphan user", "error", err, "user_id", userID)
	}
	if _, err := h.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, workspaceID); err != nil {
		h.logger.Error("cleanup orphan workspace", "error", err, "workspace_id", workspaceID)
	}
}

// Bootstrap creates the first admin user on an empty database.
// This endpoint is unauthenticated but only works when no users exist.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if len(req.FullName) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Name must be at least 2 characters"})
		return
	}
	if !emailRegex.MatchString(req.Email) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid email address"})
		return
	}
	if len(req.Password) < 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 8 characters"})
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("bootstrap: hash password", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slugBase := strings.Split(req.Email, "@")[0]
	slugBase = emailSlugCleanRE.ReplaceAllString(strings.ToLower(slugBase), "-")

	now := time.Now().UTC().Format(time.RFC3339)
	userID := generateCUID()
	workspaceID := generateCUID()
	memberID := generateCUID()

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("bootstrap: begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback()

	// Check inside tx to eliminate TOCTOU race
	var userCount int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		h.logger.Error("bootstrap: count users", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if userCount > 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Already initialized — bootstrap is only available on an empty database"})
		return
	}

	// onboarding_completed=0 on purpose: the new /bootstrap → /onboarding
	// flow runs the workspace + crew template + adapter wizard AFTER
	// the admin row exists. Pre-2026-05-13 the bootstrap handler WAS
	// the entire onboarding (it created a default workspace and that
	// was it), so this column was set to 1 unconditionally. With the
	// split-screen onboarding wizard now responsible for picking the
	// crew template and adapter, the flag must stay 0 until /onboarding/setup
	// fires — otherwise the dashboard gate sees "done" and skips
	// straight past the wizard the user just sent themselves into.
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, onboarding_completed, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert workspace", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("bootstrap: insert membership", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Generate CLI token for immediate CLI access
	tokenBytes := make([]byte, 20)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.logger.Error("bootstrap: generate token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	cliToken := cliTokenPrefix + hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(cliToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])
	tokenID := generateCUID()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		tokenID, userID, "bootstrap", tokenHashHex, now)
	if err != nil {
		h.logger.Error("bootstrap: insert cli_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("bootstrap: commit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Set browser session cookies inline so the freshly-created admin
	// lands authenticated on /onboarding without the frontend having
	// to chain a /api/auth/callback/credentials call (which was racing
	// against the auth-tier rate limiter and getting 403'd, dropping
	// the user back on /login?registered=true).
	//
	// Same pattern Signup uses: background-derived context decoupled
	// from r.Context() so a client disconnect between tx.Commit and
	// sessions.Create doesn't roll back the user we just created.
	authCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.setSessionCookies(authCtx, w, r, userID, req.FullName, req.Email); err != nil {
		h.logger.Error("bootstrap: set session cookies", "error", err)
		// Don't roll back the admin row — bootstrap can't be retried
		// once a user exists, and the user can always log in manually
		// with the password they just typed. Surface the partial
		// success so the frontend can route to /login?registered=true.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"user_id":         userID,
			"email":           req.Email,
			"workspace_id":    workspaceID,
			"cli_token":       cliToken,
			"session_pending": true,
		})
		return
	}

	h.logger.Info("bootstrap: admin created", "email", req.Email, "workspace", slug)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"user_id":      userID,
		"email":        req.Email,
		"workspace_id": workspaceID,
		"cli_token":    cliToken,
	})
}

// WsToken generates a short-lived JWE for authenticating WebSocket connections.
// POST /api/v1/auth/ws-token — works with both session cookies and CLI tokens.
//
// For browser auth: ticket carries user.SessionID so the WS hub can
// enforce server-side revocation (close 4401 if the session gets
// revoked while the WS is up).
//
// For CLI auth: ticket is issued without a session id. The WS hub's
// validator allows empty sid for kind=ws because CLI tokens have their
// own revocation table (cli_tokens) that the hub does not consult mid-
// stream — the trade-off is that revoking a CLI token does not kick
// already-open WS connections; users should disconnect them manually.
func (h *AuthHandler) WsToken(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeAuthError(w, http.StatusUnauthorized, reasonNoCredentials)
		return
	}
	// Audit H7: defensive nil check. The router only mounts AuthHandler
	// when JWTSecret is configured (so validator is non-nil at startup),
	// but a misconfigured deployment that wires the handler without a
	// validator would panic on the next line. Fail closed instead.
	if h.validator == nil {
		h.logger.Error("WsToken called without configured JWT validator")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	jweToken, err := h.validator.IssueWSTicket(user.ID, user.SessionID, user.Name, user.Email)
	if err != nil {
		h.logger.Error("issue ws ticket", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": jweToken})
}
