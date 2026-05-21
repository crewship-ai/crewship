package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

	// setupTokenMu guards setupToken. The token is created once at startup
	// when the users table is empty, logged to stderr, and consumed exactly
	// once by the first /bootstrap caller who passes it back via the
	// X-Setup-Token header. After consumption it is zeroed in memory.
	// All other states (DB already initialized, server restart after
	// bootstrap) leave setupToken == "" which makes Bootstrap unconditionally
	// refuse — no rate-limit-bypass to win the race.
	//
	// setupTokenPath is the on-disk mirror (GitLab `initial_root_password`
	// convention): the token is also written to a 0600 file under the data
	// directory so the operator can `cat` it from their SSH session instead
	// of trawling journald. Auto-deleted on successful consumption AND on
	// process restart after a successful bootstrap (the file is checked at
	// arm time; if the users table is non-empty, any leftover file gets
	// purged). Empty when MaybeGenerateSetupToken wasn't called with a
	// data dir, in which case the in-memory token still works — the file
	// is a UX add-on, not the source of truth.
	setupTokenMu   sync.Mutex
	setupToken     string
	setupTokenPath string
}

// NewAuthHandler creates an AuthHandler with the given dependencies and signup configuration.
// sessionsStore must back user_sessions (migration v63).
func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, sessionsStore sessions.Store, allowSignup bool) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator, sessions: sessionsStore, allowSignup: allowSignup}
}

// initialSetupTokenFilename mirrors the GitLab `initial_root_password`
// convention. Written under the configured data dir at mode 0600 with
// owner-only access; auto-deleted on first successful bootstrap. The
// content is just the token with a one-line header so a sysadmin who
// `cat`s the file sees what it's for and that it's one-shot.
const initialSetupTokenFilename = "initial_setup_token"

// MaybeGenerateSetupToken inspects the users table at startup. When it is
// empty, generates a one-shot setup token, stores it in memory, writes a
// mirror copy to <dataDir>/initial_setup_token (mode 0600), and logs it
// at WARN. The next caller of /api/v1/bootstrap must echo this token as
// X-Setup-Token or the request is refused.
//
// Convention follows GitLab's initial_root_password / Gitea's
// installer-token model: operator either pulls the token from the
// process log (journald, systemd, Docker logs) OR reads the file via
// SSH. Both paths reveal the same value; the file is the more
// ergonomic of the two for cloud-VM deployments where journald can be
// noisy. On successful bootstrap the in-memory token is zeroed AND the
// file is removed.
//
// dataDir mirrors the directory the secrets package uses; pass "" to
// keep the token in memory only (handler-only tests, embedded modes).
//
// When the users table is already non-empty, this also REMOVES any
// leftover initial_setup_token file from a previous incomplete
// bootstrap — keeps the on-disk state honest after the admin row
// finally lands via /signup, password reset, or an out-of-band write.
//
// Called from server.New after the DB is wired and before the HTTP server
// starts accepting traffic. Safe to call concurrently; only the first
// invocation observing an empty DB generates a token.
func (h *AuthHandler) MaybeGenerateSetupToken(ctx context.Context, dataDir string) error {
	h.setupTokenMu.Lock()
	defer h.setupTokenMu.Unlock()
	if h.setupToken != "" {
		return nil
	}
	var count int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		// Already bootstrapped — purge any leftover file from a
		// previous run that crashed between writing the file and the
		// first /bootstrap call. Leaving it on disk would be a stale
		// token + a misleading UX cue. Best-effort: a missing file is
		// fine, anything else gets logged but doesn't fail startup.
		if dataDir != "" {
			path := filepath.Join(dataDir, initialSetupTokenFilename)
			if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
				h.logger.Warn("could not remove stale setup-token file",
					"path", path, "error", rmErr)
			}
		}
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	h.setupToken = hex.EncodeToString(buf)

	// Mirror to disk if a data dir is configured. We deliberately
	// don't fail the arm step on a write error — the in-memory token
	// is the source of truth, and an operator who can read journald
	// still gets a working bootstrap. The warning makes the partial
	// state visible.
	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			h.logger.Warn("could not create data dir for setup-token file",
				"path", dataDir, "error", err)
		} else {
			path := filepath.Join(dataDir, initialSetupTokenFilename)
			content := fmt.Sprintf(
				"# crewship: one-shot bootstrap token — DO NOT COMMIT, DO NOT SHARE\n"+
					"# Use ONCE as X-Setup-Token header on POST /api/v1/bootstrap.\n"+
					"# Auto-deleted on first successful bootstrap.\n"+
					"#\n"+
					"%s\n",
				h.setupToken)
			// 0600 — owner-only read, no group, no other. Same mode as
			// secrets.env. The file lives next to it so an operator
			// who tightened access on the data dir already covers this.
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				h.logger.Warn("could not persist setup-token file",
					"path", path, "error", err,
					"fallback", "operator must read token from process log instead")
			} else {
				h.setupTokenPath = path
			}
		}
	}

	// Triple-log: stderr + slog WARN + on its own line so even a busy
	// startup log isn't likely to swallow it. The operator has to see this.
	bannerLine := strings.Repeat("=", 72)
	h.logger.Warn(bannerLine)
	h.logger.Warn("BOOTSTRAP REQUIRED — first /api/v1/bootstrap call must carry this token")
	h.logger.Warn("Send it in the X-Setup-Token header. It is shown ONCE and one-shot.")
	h.logger.Warn("CREWSHIP_BOOTSTRAP_TOKEN", "token", h.setupToken)
	if h.setupTokenPath != "" {
		h.logger.Warn("Also written to file (mode 0600)", "path", h.setupTokenPath)
	}
	h.logger.Warn(bannerLine)
	return nil
}

// consumeSetupToken returns true if the provided token matches the in-memory
// setup token AND zeroes the token so it can only be used once. Constant-time
// comparison + length pad to defeat timing oracles. Returns false (with no
// mutation) when no token is currently armed.
//
// On a successful match the on-disk mirror file (if any) is also
// removed — the bootstrap moment is the only legitimate read of the
// file. Removal is best-effort: a missing or unreachable file logs
// at WARN but does not undo the in-memory consume (the operator who
// got the bootstrap response back doesn't care that we couldn't
// rm the file; the bootstrap already succeeded).
func (h *AuthHandler) consumeSetupToken(provided string) bool {
	h.setupTokenMu.Lock()
	defer h.setupTokenMu.Unlock()
	if h.setupToken == "" {
		return false
	}
	expected := h.setupToken
	actual := provided
	// Equalise lengths so the compare runs constant-time across the
	// short/empty-input cases.
	if len(actual) != len(expected) {
		actual = strings.Repeat("\x00", len(expected))
	}
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return false
	}
	// One-shot: zero the in-memory copy so a leaked log line can't be
	// replayed against a future restart that re-armed the token.
	h.setupToken = ""
	if h.setupTokenPath != "" {
		if rmErr := os.Remove(h.setupTokenPath); rmErr != nil && !os.IsNotExist(rmErr) {
			h.logger.Warn("could not remove consumed setup-token file",
				"path", h.setupTokenPath, "error", rmErr,
				"mitigation", "operator should `rm` the file manually")
		}
		h.setupTokenPath = ""
	}
	return true
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
		replyError(w, http.StatusForbidden, "Registration is disabled. Set CREWSHIP_ALLOW_SIGNUP=true to enable.")
		return
	}

	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if len(req.FullName) < 2 {
		replyError(w, http.StatusBadRequest, "Name must be at least 2 characters")
		return
	}
	if !emailRegex.MatchString(req.Email) {
		replyError(w, http.StatusBadRequest, "Invalid email address")
		return
	}
	if len(req.Password) < 8 {
		replyError(w, http.StatusBadRequest, "Password must be at least 8 characters")
		return
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email = ?", req.Email).Scan(&existingID)
	if err == nil {
		replyError(w, http.StatusConflict, "Email already registered")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check existing email", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("hash password", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("insert user", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("insert workspace", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("insert membership", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Failed to establish session — please try again")
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
//
// This endpoint is unauthenticated but gated by a one-shot setup token
// generated at process startup whenever the users table is empty
// (see MaybeGenerateSetupToken). The token is logged to stderr ONCE
// and must be echoed back in the X-Setup-Token header on first call.
//
// Without the gate, any process that reached the public listener could
// race the legitimate operator to be the first POST on an empty DB and
// walk away with an OWNER + CLI token. The deploy race was demonstrated
// against dev1 during the 2026-05-21 audit.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	// Setup-token gate first. We deliberately validate it BEFORE input
	// parsing so an attacker scanning for the endpoint can't even
	// fingerprint validation errors without already holding the token.
	provided := strings.TrimSpace(r.Header.Get("X-Setup-Token"))
	if !h.consumeSetupToken(provided) {
		h.logger.Warn("bootstrap: setup token check failed",
			"remote_addr", r.RemoteAddr,
			"token_present", provided != "",
			"user_agent", r.Header.Get("User-Agent"))
		// 403 mirrors the post-bootstrap "Already initialized" message —
		// from the caller's perspective the bootstrap path is simply
		// closed. We do not distinguish "wrong token" from "no token
		// armed" to avoid leaking whether the server is mid-deploy.
		replyError(w, http.StatusForbidden, "Bootstrap closed — setup token required")
		return
	}

	var req signupRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if len(req.FullName) < 2 {
		replyError(w, http.StatusBadRequest, "Name must be at least 2 characters")
		return
	}
	if !emailRegex.MatchString(req.Email) {
		replyError(w, http.StatusBadRequest, "Invalid email address")
		return
	}
	if len(req.Password) < 8 {
		replyError(w, http.StatusBadRequest, "Password must be at least 8 characters")
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		h.logger.Error("bootstrap: hash password", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	// Check inside tx to eliminate TOCTOU race
	var userCount int
	if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		h.logger.Error("bootstrap: count users", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if userCount > 0 {
		replyError(w, http.StatusForbidden, "Already initialized — bootstrap is only available on an empty database")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert workspace", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		h.logger.Error("bootstrap: insert membership", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Generate CLI token for immediate CLI access
	tokenBytes := make([]byte, 20)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.logger.Error("bootstrap: generate token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("bootstrap: commit", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	jweToken, err := h.validator.IssueWSTicket(user.ID, user.SessionID, user.Name, user.Email)
	if err != nil {
		h.logger.Error("issue ws ticket", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": jweToken})
}
