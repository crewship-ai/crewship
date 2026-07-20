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
	"github.com/crewship-ai/crewship/internal/mailer"
)

// AuthHandler provides user authentication endpoints including signup, login, and WebSocket token exchange.
type AuthHandler struct {
	db          *sql.DB
	logger      *slog.Logger
	validator   *auth.JWTValidator
	sessions    sessions.Store
	allowSignup bool

	// mail carries the out-of-band half of the de-enumerated signup:
	// the "you already have an account" notice. Optional — nil (tests)
	// and mailer.Disabled{} (no transport configured) both degrade to
	// a log line, never to a different HTTP response. Wired by
	// NewRouter alongside the recovery handler's mailer.
	mail mailer.Mailer

	// bootstrap window state gates POST /api/v1/bootstrap on an empty
	// database. Armed by ArmDeployRaceWindow at server start.
	//
	// DEFAULT (bootstrapNoExpiry=true): the window stays open until the
	// first admin exists — the empty users table is the gate, not a clock.
	// This matches the GitLab/Grafana first-run pattern: the operator hits
	// /bootstrap from a browser whenever they get to it. The window becomes
	// moot the moment users.count > 0.
	//
	// OPT-IN hardening (CREWSHIP_BOOTSTRAP_WINDOW=<duration> → deadline set):
	// arms a FINITE window that refuses /bootstrap after the deadline, so an
	// internet-reachable instance nobody bootstrapped doesn't sit open to
	// whichever scanner finds the URL first. For public deploys that want
	// the deploy-race protection.
	//
	// Four states matter:
	//   bootstrapArmed=false                   — never attempted (test harness)
	//                                            or arming failed (fail-closed)
	//   armed=true, noExpiry=true              — open until first admin (default)
	//   armed=true, deadline set (future)      — open until that deadline (opt-in)
	//   armed=true, noExpiry=false, deadline   — window consumed by a successful
	//     zero (or past)                         bootstrap, expired, or users
	//                                            table already populated at arm
	//
	// Without the explicit `armed` flag, a transient DB error during
	// ArmDeployRaceWindow would leave deadline=zero and the handler
	// would fail-open (treat it as "no window armed = allow"). The
	// flag lets bootstrapWindowOpen distinguish "intentionally unarmed"
	// (allow, dev-mode) from "arming failed" (refuse, fail-closed).
	bootstrapMu        sync.Mutex
	bootstrapArmed     bool
	bootstrapNoExpiry  bool
	bootstrapDeadline  time.Time
	bootstrapArmingErr error
}

// NewAuthHandler creates an AuthHandler with the given dependencies and signup configuration.
// sessionsStore must back user_sessions (migration v63).
func NewAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, sessionsStore sessions.Store, allowSignup bool) *AuthHandler {
	return &AuthHandler{db: db, logger: logger, validator: validator, sessions: sessionsStore, allowSignup: allowSignup}
}

// ArmDeployRaceWindow opens the bootstrap window when the users table
// is empty. Called from server.New before the HTTP listener accepts
// traffic.
//
// window <= 0 (the default): NO-EXPIRY mode — bootstrap stays open
// until the first admin exists. The empty users table is the gate, so
// the operator can open the /bootstrap URL whenever they get to it
// (GitLab/Grafana first-run behaviour). This is what unset
// CREWSHIP_BOOTSTRAP_WINDOW yields.
//
// window > 0: FINITE deploy-race window (opt-in via
// CREWSHIP_BOOTSTRAP_WINDOW=<duration>). Bootstrap is open only for
// that interval after startup; afterwards the handler refuses so an
// internet-reachable instance nobody bootstrapped doesn't sit open to
// whichever scanner finds the URL first. Headless / CI deploys hit
// `crewship init` against the same endpoint.
//
// When users.count > 0 this is a no-op (armed=true, no-expiry off,
// deadline=zero): bootstrap is already closed because Bootstrap itself
// refuses with 410 once a user exists.
//
// On error (e.g. transient DB blip): armed stays false and the
// stored error is preserved. bootstrapWindowOpen() then fails closed
// — the handler refuses rather than treating the unset deadline as
// "no window so allow". This is the security-conservative choice for
// the deploy-race vector the window exists to defend against.
func (h *AuthHandler) ArmDeployRaceWindow(ctx context.Context, window time.Duration) error {
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
		h.bootstrapNoExpiry = false
		h.bootstrapDeadline = time.Time{}
		h.bootstrapArmingErr = nil
		h.bootstrapMu.Unlock()
		return nil
	}
	h.bootstrapMu.Lock()
	h.bootstrapArmed = true
	h.bootstrapArmingErr = nil
	if window <= 0 {
		// Default: open until the first admin exists, no deadline.
		h.bootstrapNoExpiry = true
		h.bootstrapDeadline = time.Time{}
	} else {
		// Opt-in finite deploy-race window.
		h.bootstrapNoExpiry = false
		h.bootstrapDeadline = time.Now().Add(window)
	}
	deadline := h.bootstrapDeadline
	noExpiry := h.bootstrapNoExpiry
	h.bootstrapMu.Unlock()

	publicURL := strings.TrimRight(os.Getenv("CREWSHIP_PUBLIC_URL"), "/")
	if publicURL == "" {
		// Fall back to the instance's actual port, not a hardcoded 8080 —
		// multi-instance dev (dev.sh exports CREWSHIP_PORT=808N) printed a
		// banner URL pointing at a different instance's bootstrap page.
		port := os.Getenv("CREWSHIP_PORT")
		if port == "" {
			port = "8080"
		}
		publicURL = "http://localhost:" + port
	}
	bannerLine := strings.Repeat("─", 72)
	h.logger.Warn(bannerLine)
	h.logger.Warn("  Crewship first run — bootstrap your admin account.")
	h.logger.Warn("")
	h.logger.Warn("  Open this URL in your browser and fill in the form:")
	h.logger.Warn("       " + publicURL + "/bootstrap")
	h.logger.Warn("")
	if noExpiry {
		h.logger.Warn("  This window stays open until you create the admin account.")
		h.logger.Warn("  Or use 'crewship init' from this host for headless setup.")
	} else {
		h.logger.Warn("  Window closes at: " + deadline.Format(time.RFC3339))
		h.logger.Warn("  After that, restart the server to arm a new window,")
		h.logger.Warn("  or use 'crewship init' from this host for headless setup.")
	}
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
	if h.bootstrapNoExpiry {
		// Default mode: open until the first admin exists. The Bootstrap
		// handler's users.count > 0 short-circuit is what ultimately
		// closes it; closeBootstrapWindow clears this on success.
		return true
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

// closeBootstrapWindow zeroes the deadline AND clears no-expiry mode so
// subsequent bootstrap calls return 410 — even in the default open-until-
// admin mode and even if they arrive before any original deadline.
// Called by Bootstrap on success — one-shot semantics.
func (h *AuthHandler) closeBootstrapWindow() {
	h.bootstrapMu.Lock()
	defer h.bootstrapMu.Unlock()
	h.bootstrapNoExpiry = false
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

// Signup registers a new user and creates their default workspace.
// POST /api/v1/auth/signup — disabled when CREWSHIP_ALLOW_SIGNUP is false.
//
// The response is deliberately the same 202 + body for a brand-new
// address and for one that already has an account: the pre-2026-07
// 409 "Email already registered" turned this endpoint into an email
// enumeration oracle for anyone who could reach it. Login (dummy-
// bcrypt timing equalizer) and /auth/forgot (always 200) were already
// de-enumerated; signup was the last one left.
//
// Consequences of that contract, all intentional:
//
//   - No session cookie. Signup used to log the new user straight in,
//     but Set-Cookie is part of the response — emitting it only on the
//     created path leaks existence exactly as loudly as the 409 did.
//     The dashboard now routes to /login?signup=submitted instead.
//   - No account id in the body, for the same reason.
//   - The collision is handled out-of-band the way recovery does it:
//     an "account already exists" notice to the address itself when a
//     mailer is configured, an info log with a hashed address when not.
//   - Both paths burn one bcrypt at cost 12 so the response time
//     doesn't answer the question the body refuses to.
//
// Residual, documented in docs/security/threat-model.mdx: with open
// signup and no email-verification step, an attacker who signs up with
// victim@example.com can still infer the answer by trying to log in
// with the password they just chose. Closing that needs verification-
// before-activation, which is tracked separately; this change removes
// the single-request oracle.
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
		// Address is taken. Spend one bcrypt so this path costs what
		// the create path costs (GenerateFromPassword at the same cost
		// 12 below), then answer exactly as if we had created the
		// account. The owner hears about the attempt by email.
		_ = bcryptCompareHashAndPassword(dummyBcryptHash(), req.Password)
		h.notifyExistingAccount(r.Context(), req.Email)
		writeSignupResponse(w)
		return
	}
	if err != sql.ErrNoRows {
		replyInternalError(w, h.logger, "check existing email", err)
		return
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		replyInternalError(w, h.logger, "hash password", err)
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
		replyInternalError(w, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO users (id, full_name, email, hashed_password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		userID, req.FullName, req.Email, string(hashed), now, now)
	if err != nil {
		// users.email is UNIQUE, so a concurrent signup for the same
		// address can land between our probe above and this INSERT.
		// That failure is existence-dependent — surfacing it as a 500
		// would put the oracle back, this time in the status code.
		if isDuplicateEmailErr(err) {
			h.logger.Info("signup: lost the insert race for an existing address",
				"email_hash", emailHashShort(req.Email))
			writeSignupResponse(w)
			return
		}
		replyInternalError(w, h.logger, "insert user", err)
		return
	}

	slug := slugBase + "-" + workspaceID[:8]
	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspaces (id, name, slug, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		workspaceID, req.FullName+"'s Workspace", slug, now, now)
	if err != nil {
		replyInternalError(w, h.logger, "insert workspace", err)
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		replyInternalError(w, h.logger, "insert membership", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit tx", err)
		return
	}

	h.logger.Info("signup: account created", "user_id", userID, "workspace", slug)
	writeSignupResponse(w)
}

// writeSignupResponse writes the no-enumeration signup response. Single
// helper — like the recovery handler's writeForgotResponse — so every
// path answers with byte-identical bytes and nobody can reintroduce a
// distinguishing branch by accident.
func writeSignupResponse(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"message": "If that email address isn't already registered, the account has been created. Sign in at /login to continue.",
	})
}

// isDuplicateEmailErr reports whether err is the UNIQUE violation on
// users.email. Matched on the driver's message because modernc/sqlite
// surfaces constraint failures as an opaque error type whose code we'd
// have to import the driver to inspect — and the api package
// deliberately stays driver-agnostic.
func isDuplicateEmailErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") && strings.Contains(msg, "users.email")
}

// notifyExistingAccount is the out-of-band half of the de-enumerated
// signup: the HTTP response can't say "you already have an account",
// so we tell the address itself. Mirrors /auth/forgot — when no mailer
// is configured (the common self-hosted case) we only log, with the
// address hashed so the log isn't its own enumeration surface.
//
// Errors are swallowed on purpose: the caller has already decided what
// to answer, and a send failure must not change the response.
func (h *AuthHandler) notifyExistingAccount(ctx context.Context, email string) {
	if h.mail == nil || !h.mail.Configured() {
		h.logger.Info("signup: attempt on an address that already has an account (no mailer configured, owner not notified)",
			"email_hash", emailHashShort(email))
		return
	}
	// Decouple from the request: the response is about to go out and
	// nothing the mailer does should be able to hold it up beyond this.
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := h.mail.Send(sendCtx, mailer.Message{
		To:      email,
		Subject: "Someone tried to sign up with your Crewship email",
		HTML:    existingAccountEmailHTML(),
		Text:    existingAccountEmailText(),
	}); err != nil {
		h.logger.Error("signup: notify existing account failed", "error", err, "email_hash", emailHashShort(email))
	}
}

func existingAccountEmailHTML() string {
	return `<!doctype html>
<html><body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:560px;margin:0 auto;padding:32px;color:#18181b;">
<h2 style="margin:0 0 16px 0;font-size:20px;">You already have a Crewship account</h2>
<p>Someone (hopefully you) just tried to create a Crewship account with this email address. One already exists, so nothing was changed.</p>
<p>If that was you, sign in with your existing password — or use the "Forgot password" link if you don't remember it.</p>
<p style="color:#71717a;font-size:13px;">If it wasn't you, no action is needed: your password was not changed and no new account was created.</p>
</body></html>`
}

func existingAccountEmailText() string {
	return `Someone (hopefully you) just tried to create a Crewship account with this email address.

One already exists, so nothing was changed.

If that was you, sign in with your existing password — or use the "Forgot password" link if you don't remember it.

If it wasn't you, no action is needed: your password was not changed and no new account was created.`
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
		replyInternalError(w, h.logger, "bootstrap: hash password", err)
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
		replyInternalError(w, h.logger, "bootstrap: begin tx", err)
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
		replyInternalError(w, h.logger, "bootstrap: insert user", err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "bootstrap: rows affected", err)
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
		replyInternalError(w, h.logger, "bootstrap: insert workspace", err)
		return
	}

	_, err = tx.ExecContext(r.Context(),
		"INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?)",
		memberID, workspaceID, userID, "OWNER", now)
	if err != nil {
		replyInternalError(w, h.logger, "bootstrap: insert membership", err)
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
		replyInternalError(w, h.logger, "bootstrap: generate token", err)
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
		replyInternalError(w, h.logger, "bootstrap: insert cli_token", err)
		return
	}

	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "bootstrap: commit", err)
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
// GET /api/v1/ws-token — works with both session cookies and CLI tokens.
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
		replyInternalError(w, h.logger, "issue ws ticket", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": jweToken})
}
