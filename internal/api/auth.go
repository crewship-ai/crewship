package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
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

	// bootstrapDeadline is the timestamp after which POST /api/v1/bootstrap
	// stops accepting requests on an empty database. Set by ArmDeployRaceWindow
	// at server start (defaults to 5 minutes) — a fixed first-run window
	// pattern. The operator hits /bootstrap from a browser, completes the
	// form, and the deadline becomes moot the moment users.count > 0. If
	// the window elapses without a bootstrap, the server starts refusing
	// /bootstrap requests with a clear "expired, please restart" error so
	// an internet-reachable instance that nobody bootstrapped doesn't sit
	// permanently open to whichever scanner finds the URL first.
	//
	// Three states matter:
	//   bootstrapArmed=false                — never attempted (test harness)
	//                                         or arming failed (fail-closed)
	//   bootstrapArmed=true,  deadline set  — window open until deadline
	//   bootstrapArmed=true,  deadline zero — window consumed by a
	//                                         successful bootstrap, or
	//                                         users table was already
	//                                         populated at arm time
	//
	// Without the explicit `armed` flag, a transient DB error during
	// ArmDeployRaceWindow would leave deadline=zero and the handler
	// would fail-open (treat it as "no window armed = allow"). The
	// flag lets bootstrapWindowOpen distinguish "intentionally unarmed"
	// (allow, dev-mode) from "arming failed" (refuse, fail-closed).
	bootstrapMu        sync.Mutex
	bootstrapArmed     bool
	bootstrapDeadline  time.Time
	bootstrapArmingErr error
}

// NewAuthHandler creates an AuthHandler with the given dependencies and signup configuration.
// sessionsStore must back user_sessions (migration v63).
func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, sessionsStore sessions.Store, allowSignup bool) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator, sessions: sessionsStore, allowSignup: allowSignup}
}

// defaultBootstrapWindow matches Portainer's 5-minute first-run window
// — long enough for a human operator to open the URL after starting
// the server, short enough that an unbootstrapped instance left
// running on a public IP doesn't sit indefinitely open.
const defaultBootstrapWindow = 5 * time.Minute

// ArmDeployRaceWindow opens the bootstrap window for the configured
// duration when the users table is empty. Called from server.New
// before the HTTP listener accepts traffic.
//
// Fixed-duration first-run window: bootstrap is open for a known
// interval after startup; outside that window the handler refuses.
// The operator who started the server is expected to open the
// bootstrap URL within the window — typically seconds after
// `crewship start`. An operator who needs a longer window passes a
// larger duration; headless / CI deploys hit `crewship init` against
// the same endpoint within the window.
//
// When users.count > 0 this is a no-op (armed=true, deadline=zero):
// bootstrap is already closed because Bootstrap itself refuses with
// 410 once a user exists.
//
// On error (e.g. transient DB blip): armed stays false and the
// stored error is preserved. bootstrapWindowOpen() then fails closed
// — the handler refuses rather than treating the unset deadline as
// "no window so allow". This is the security-conservative choice for
// the deploy-race vector the window exists to defend against.
func (h *AuthHandler) ArmDeployRaceWindow(ctx context.Context, window time.Duration) error {
	if window <= 0 {
		window = defaultBootstrapWindow
	}
	var count int
	if err := h.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		// Fail-closed: record the error so subsequent bootstrap
		// requests refuse instead of falling through to "no window
		// armed = allow". The operator can fix the DB and restart.
		h.bootstrapMu.Lock()
		h.bootstrapArmed = false
		h.bootstrapArmingErr = err
		h.bootstrapMu.Unlock()
		return err
	}
	if count > 0 {
		// Already bootstrapped — mark armed with zero deadline so
		// bootstrapWindowOpen() returns false ("consumed") rather
		// than the dev-only "never armed" branch. Bootstrap handler
		// also short-circuits on users.count > 0, so both layers
		// agree: bootstrap is closed.
		h.bootstrapMu.Lock()
		h.bootstrapArmed = true
		h.bootstrapDeadline = time.Time{}
		h.bootstrapArmingErr = nil
		h.bootstrapMu.Unlock()
		return nil
	}
	h.bootstrapMu.Lock()
	h.bootstrapArmed = true
	h.bootstrapDeadline = time.Now().Add(window)
	h.bootstrapArmingErr = nil
	deadline := h.bootstrapDeadline
	h.bootstrapMu.Unlock()

	publicURL := strings.TrimRight(os.Getenv("CREWSHIP_PUBLIC_URL"), "/")
	if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	bannerLine := strings.Repeat("─", 72)
	h.logger.Warn(bannerLine)
	h.logger.Warn("  Crewship first run — bootstrap your admin account.")
	h.logger.Warn("")
	h.logger.Warn("  Open this URL in your browser and fill in the form:")
	h.logger.Warn("       " + publicURL + "/bootstrap")
	h.logger.Warn("")
	h.logger.Warn("  Window closes at: " + deadline.Format(time.RFC3339))
	h.logger.Warn("  After that, restart the server to arm a new window,")
	h.logger.Warn("  or use 'crewship init' from this host for headless setup.")
	h.logger.Warn(bannerLine)
	return nil
}

// bootstrapWindowOpen reports whether the deploy-race window is still
// open AND was successfully armed.
//
// Returns true ONLY when ArmDeployRaceWindow ran without error AND
// the deadline is still in the future. Two false paths:
//   - armed=false        → arming failed or was never attempted. Refuse.
//     Combined with the dev/test "never armed" state, that's still
//     the safer fail-closed branch: production always arms, so a
//     non-armed state in prod can only mean DB failure.
//   - armed=true, deadline=zero or in the past → window consumed
//     (successful bootstrap closed it) or expired.
//
// Callers also need to short-circuit on users.count > 0 separately —
// the window state says nothing about whether the instance was
// actually bootstrapped; both are independent gates.
func (h *AuthHandler) bootstrapWindowOpen() bool {
	h.bootstrapMu.Lock()
	defer h.bootstrapMu.Unlock()
	if !h.bootstrapArmed {
		return false
	}
	if h.bootstrapDeadline.IsZero() {
		return false
	}
	return time.Now().Before(h.bootstrapDeadline)
}

// bootstrapArmingFailed reports whether the most recent
// ArmDeployRaceWindow call returned an error (e.g. transient DB
// failure). Used by the Bootstrap handler to surface a distinct 503
// when the cause is "we couldn't probe the DB" rather than the
// generic 410 "expired" — operators benefit from the actionable
// error message and we keep fail-closed on the security side.
func (h *AuthHandler) bootstrapArmingFailed() bool {
	h.bootstrapMu.Lock()
	defer h.bootstrapMu.Unlock()
	return !h.bootstrapArmed && h.bootstrapArmingErr != nil
}

// closeBootstrapWindow zeroes the deadline so subsequent bootstrap
// calls return 410 even if they arrive before the original deadline.
// Called by Bootstrap on success — one-shot semantics.
func (h *AuthHandler) closeBootstrapWindow() {
	h.bootstrapMu.Lock()
	defer h.bootstrapMu.Unlock()
	h.bootstrapDeadline = time.Time{}
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
// This endpoint is unauthenticated; defense against the deploy-race
// (a LAN-reachable scanner racing the operator to be the first POST)
// is threefold:
//
//  1. Fixed-duration first-run window: bootstrap is only open for a
//     known interval after server start (default 5 minutes — see
//     ArmDeployRaceWindow). After the deadline the handler refuses
//     with 410. The operator who started the server is expected to
//     hit the form within seconds; anyone arriving 5 minutes later
//     is by definition not the operator.
//
//  2. Fail-closed on arming failure: if ArmDeployRaceWindow couldn't
//     probe the users table at startup (transient DB blip), the
//     window stays "not armed" and the handler returns 503 rather
//     than falling through to "no window = allow". The operator
//     restarts the server once the DB is healthy.
//
//  3. One-shot semantics at the DB boundary: the transaction at the
//     bottom runs COUNT(*) on users inside the tx and aborts on >0,
//     so two concurrent POSTs both seeing "no users yet" outside the
//     tx still serialise on the SQLite write lock — exactly one
//     commits, the other 410s. closeBootstrapWindow zeroes the
//     in-memory deadline too so a third sibling can't slip through
//     the pre-tx window check after the first commit lands.
//
// Headless / CI path: `crewship init --server <url> --email … --name …`
// is the CLI wrapper around this endpoint, useful for Ansible, Docker
// Compose, and provisioning scripts that can't open a browser. It
// hits the same window + race protections.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	// Fast 410 path: already bootstrapped (users.count > 0). Returned
	// before the window check so a re-POST gets an actionable "log in"
	// message instead of a generic "window closed" one. This is a
	// pre-tx hint only; the authoritative check is inside the
	// transaction at the bottom.
	var existingCount int
	if err := h.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM users").Scan(&existingCount); err == nil && existingCount > 0 {
		replyError(w, http.StatusGone, "Already initialized — please log in at /login instead")
		return
	}
	// Fail-closed on arming failure: distinguish a real DB error at
	// arm time from "never armed" (test harness) so production refuses
	// rather than falling through.
	if h.bootstrapArmingFailed() {
		h.logger.Warn("bootstrap: refused — deploy-race window arming failed",
			"remote_addr", r.RemoteAddr)
		replyError(w, http.StatusServiceUnavailable, "Bootstrap arming failed at server startup — restart the server once the database is reachable")
		return
	}
	// Deploy-race window check. A zero deadline + armed=true means the
	// window was consumed or the DB was already populated at arm time;
	// 410 with the "expired" message lets the operator restart. A
	// "never armed" state (test harness) falls through unconditionally,
	// which is OK because production always arms.
	if !h.bootstrapWindowOpen() {
		h.bootstrapMu.Lock()
		armed := h.bootstrapArmed
		h.bootstrapMu.Unlock()
		if armed {
			h.logger.Warn("bootstrap: refused — deploy-race window expired or already consumed",
				"remote_addr", r.RemoteAddr, "user_agent", r.Header.Get("User-Agent"))
			replyError(w, http.StatusGone, "Bootstrap window expired — restart the server to open a new one")
			return
		}
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

	// onboarding_completed=0 on purpose: the new /bootstrap → /onboarding
	// flow runs the workspace + crew template + adapter wizard AFTER
	// the admin row exists. Pre-2026-05-13 the bootstrap handler WAS
	// the entire onboarding (it created a default workspace and that
	// was it), so this column was set to 1 unconditionally. With the
	// split-screen onboarding wizard now responsible for picking the
	// crew template and adapter, the flag must stay 0 until /onboarding/setup
	// fires — otherwise the dashboard gate sees "done" and skips
	// straight past the wizard the user just sent themselves into.
	//
	// `WHERE NOT EXISTS (SELECT 1 FROM users)` makes the insert
	// itself the singleton gate. Two concurrent POSTs both pass the
	// pre-tx and in-tx COUNT checks (their snapshots see no users)
	// but only one INSERT actually writes a row — the other gets
	// RowsAffected=0 and we 410. This closes the deploy-race even
	// when the two POSTs arrive with different emails (no UNIQUE
	// constraint conflict to lean on).
	res, err := tx.ExecContext(r.Context(),
		`INSERT INTO users (id, full_name, email, hashed_password, onboarding_completed, created_at, updated_at)
		 SELECT ?, ?, ?, ?, 0, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM users)`,
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		h.logger.Error("bootstrap: insert user", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("bootstrap: rows affected", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		// A concurrent caller won the race between our pre-tx COUNT
		// and this INSERT. Their row is the authoritative admin;
		// ours would be a duplicate that the singleton guard
		// silently dropped.
		replyError(w, http.StatusGone, "Already initialized — please log in at /login instead")
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

	// Generate CLI token for immediate CLI access. 32 bytes = 256-bit
	// entropy, matching CLITokenHandler.Create (Patch J). Pre-M6 this
	// path minted 20-byte (160-bit) tokens — a live-test inconsistency
	// caught by the A/B run against dev1 8084. Bootstrap is a single
	// one-shot operation and the issued token is the FIRST admin
	// credential on a new install, so entropy parity with the rest of
	// the token surface is the right default.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.logger.Error("bootstrap: generate token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	cliToken := cliTokenPrefix + hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(cliToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])
	tokenID := generateCUID()

	// Bootstrap tokens explicitly tier='STANDARD' — the bootstrap
	// admin lands as workspace OWNER and can mint themselves an
	// ADMIN-tier token afterward via /api/v1/auth/cli-token. We
	// don't auto-issue an admin token here because the bootstrap
	// flow is unauthenticated up to this point; the user hasn't
	// agreed to any 7-day expiry contract yet.
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at) VALUES (?, ?, ?, ?, 'STANDARD', ?)",
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

	// One-shot: close the deploy-race window so a second POST gets 410
	// even if it races us inside the original deadline.
	h.closeBootstrapWindow()

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
