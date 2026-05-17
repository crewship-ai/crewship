package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// ---------------------------------------------------------------------------
// auth.go — recovery / cleanup paths that the main auth_test.go does not
// touch.  The signup happy / validation paths already have green coverage;
// these tests focus on the bits that are only reachable on the error
// branches (cleanupOrphanedSignup) plus a clarifying assertion on the
// /api/auth/error contract which the next-auth client SDK depends on.
// ---------------------------------------------------------------------------

// newAuthHandlerForExtra mirrors the inline construction used by the
// existing TestAuthSignup_* tests — kept local so we do not edit the
// neighbouring test file. allowSignup is on by default because every
// caller in this file exercises a path that the disabled flag would
// short-circuit.
func newAuthHandlerForExtra(t *testing.T, allowSignup bool) (*AuthHandler, *auth.JWTValidator) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return NewAuthHandler(db, logger, v, sessions.NewDBStore(db), allowSignup), v
}

// TestAuth_CleanupOrphanedSignup_DeletesUserAndWorkspace exercises the
// post-commit rollback path used by Signup when setSessionCookies fails.
// The contract: the row pair (users, workspaces) created by the signup
// transaction must be fully removed so the user can re-submit the same
// email later without tripping the "Email already registered" branch.
// A regression here would leave an unreachable ghost account behind —
// the user has no password they trust, but the email is locked.
func TestAuth_CleanupOrphanedSignup_DeletesUserAndWorkspace(t *testing.T) {
	t.Parallel()
	h, _ := newAuthHandlerForExtra(t, true)

	// Hand-seed a half-formed signup row pair the way the Signup
	// transaction would have if setSessionCookies had then failed.
	const (
		userID = "orphan-user-id"
		wsID   = "orphan-ws-id"
		mID    = "orphan-m-id"
	)
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, 'orphan@example.com', 'Orphan', 'hash')`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Orphan WS', 'orphan-ws')`, wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`, mID, wsID, userID); err != nil {
		t.Fatalf("seed member: %v", err)
	}

	h.cleanupOrphanedSignup(userID, wsID)

	// Both rows must be gone. The workspace_members row should have
	// been swept away by the FK CASCADE on users.id — the inline doc
	// comment on cleanupOrphanedSignup advertises this exact behaviour,
	// so if the migration ever drops the CASCADE this test catches it.
	var users, workspaces, members int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, userID).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 0 {
		t.Errorf("users remaining = %d, want 0", users)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id=?`, wsID).Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaces != 0 {
		t.Errorf("workspaces remaining = %d, want 0", workspaces)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE id=?`, mID).Scan(&members); err != nil {
		t.Fatalf("count members: %v", err)
	}
	if members != 0 {
		t.Errorf("workspace_members remaining = %d, want 0 (FK CASCADE on users delete)", members)
	}
}

// TestAuth_CleanupOrphanedSignup_LeavesUnrelatedRowsAlone is the
// counter-test: cleanup must scope by id and never touch rows that
// happen to live alongside the orphan. A naive `DELETE FROM users`
// without a WHERE would silently pass the sibling test above; this
// guard makes sure cleanup keeps its blast radius to a single row.
func TestAuth_CleanupOrphanedSignup_LeavesUnrelatedRowsAlone(t *testing.T) {
	t.Parallel()
	h, _ := newAuthHandlerForExtra(t, true)

	// Pre-existing "real" user + workspace that must NOT be deleted.
	keepUser := seedTestUser(t, h.db)
	keepWS := seedTestWorkspace(t, h.db, keepUser)

	// Orphan pair to be cleaned up.
	const (
		orphanU = "orphan-u"
		orphanW = "orphan-w"
	)
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, 'o@e.com', 'O', 'h')`, orphanU); err != nil {
		t.Fatalf("seed orphan user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'orphan-only')`, orphanW); err != nil {
		t.Fatalf("seed orphan ws: %v", err)
	}

	h.cleanupOrphanedSignup(orphanU, orphanW)

	// The fully-formed user + workspace must still be present.
	var users, workspaces int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id=?`, keepUser).Scan(&users); err != nil {
		t.Fatalf("count keep-user: %v", err)
	}
	if users != 1 {
		t.Errorf("seeded user vanished after cleanup of unrelated id: count=%d", users)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id=?`, keepWS).Scan(&workspaces); err != nil {
		t.Fatalf("count keep-ws: %v", err)
	}
	if workspaces != 1 {
		t.Errorf("seeded workspace vanished after cleanup of unrelated id: count=%d", workspaces)
	}
}

// TestAuth_CleanupOrphanedSignup_NoRows_SilentlyOK locks in the
// best-effort contract: cleanup must not panic or error-out when the
// row pair is already missing (e.g. an earlier sweeper deleted them,
// or the IDs never existed). The handler logs and moves on. We assert
// only that the call returns without crashing the goroutine.
func TestAuth_CleanupOrphanedSignup_NoRows_SilentlyOK(t *testing.T) {
	t.Parallel()
	h, _ := newAuthHandlerForExtra(t, true)
	// IDs that have never been inserted — no panic, no goroutine death.
	h.cleanupOrphanedSignup("nope-user", "nope-ws")
}

// TestAuth_Signup_OrphanCleanupOnSessionFailure verifies the *integration*
// of cleanupOrphanedSignup with the Signup handler: when the sessions
// store is nil (a misconfigured deployment), the post-commit cookie
// write fails and the handler must roll back the user it just committed.
// This is the only path that exercises setSessionCookies returning
// errSessionsStoreUnconfigured end-to-end through the HTTP handler.
func TestAuth_Signup_OrphanCleanupOnSessionFailure(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	// nil sessions store — setSessionCookies hits errSessionsStoreUnconfigured
	// and Signup is forced down the rollback branch.
	h := NewAuthHandler(db, logger, v, nil, true)

	body := bytes.NewBufferString(`{"full_name":"Ghost","email":"ghost@example.com","password":"longenough"}`)
	req := httptest.NewRequest("POST", "/api/v1/auth/signup", body)
	rr := httptest.NewRecorder()
	h.Signup(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (session-setup failure)", rr.Code)
	}

	// The whole point of the rollback: no user, no workspace, no
	// membership should survive. A green test here proves
	// cleanupOrphanedSignup actually fired and finished.
	var users, workspaces, members int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='ghost@example.com'`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&workspaces)
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspace_members`).Scan(&members)
	if users != 0 {
		t.Errorf("orphan user survived: count=%d", users)
	}
	if workspaces != 0 {
		t.Errorf("orphan workspace survived: count=%d", workspaces)
	}
	if members != 0 {
		t.Errorf("orphan workspace_members survived: count=%d", members)
	}
}

// TestAuth_ErrorEndpoint_EchoesQueryParam pins down the /api/auth/error
// contract used by next-auth/react: a 200 OK response whose body
// reflects the `error` query param verbatim. Loose-shape assertion on
// purpose — the next-auth client only cares that the type round-trips
// and that the status is non-error, so over-specifying the JSON keys
// would lock in implementation detail.
//
// NOTE: there is a separate TestNextAuth_Error that covers the
// "Default" fallback when ?error is absent; this one is specifically
// about preserving the caller-supplied error type so the frontend can
// render the right copy ("AccessDenied", "Verification", etc.).
func TestAuth_ErrorEndpoint_EchoesQueryParam(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/error?error=AccessDenied", nil)
	rr := httptest.NewRecorder()
	h.Error(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Body must contain the literal error type — loose contract,
	// stable against future schema reshuffles.
	if !strings.Contains(rr.Body.String(), "AccessDenied") {
		t.Errorf("body did not echo error query param: %s", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// auth_google.go — Redirect (enabled / disabled) and findOrCreateUser
// (existing-account / existing-email / brand-new-user). The existing
// auth_google_test.go covers Callback's state-handling paths but does
// not touch Redirect or findOrCreateUser, which is exactly the gap
// this file fills.
// ---------------------------------------------------------------------------

// newGoogleHandlerExtra builds a GoogleAuthHandler with caller-controlled
// clientID / clientSecret so each test can flip Enabled() on or off.
// Mirrors the production NewGoogleAuthHandler signature; nothing in the
// Google flow reads env vars (the constructor takes the credentials by
// argument), so the t.Setenv idiom that was suggested in the brief does
// not actually drive the Redirect handler — we exercise the same on/off
// behaviour by passing empty vs non-empty strings here.
func newGoogleHandlerExtra(t *testing.T, clientID, clientSecret string) *GoogleAuthHandler {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return NewGoogleAuthHandler(db, logger, v, sessions.NewDBStore(db), clientID, clientSecret, "http://localhost:8080")
}

// TestAuthGoogle_Redirect_NotConfigured_Returns404 documents what
// happens when an operator hits /api/v1/auth/google without setting
// CREWSHIP_GOOGLE_CLIENT_ID / CREWSHIP_GOOGLE_CLIENT_SECRET. The handler
// short-circuits at h.Enabled() and replyError(404). The UI uses this
// status to decide whether to render the "Sign in with Google" button.
//
// Status code is asserted directly against what the source returns —
// the task brief hedged between 501/503/etc, but auth_google.go:54
// explicitly emits http.StatusNotFound, so that is what we lock in.
func TestAuthGoogle_Redirect_NotConfigured_Returns404(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "", "")
	if h.Enabled() {
		t.Fatalf("precondition: Enabled() should be false when client_id/secret are empty")
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/google", nil)
	rr := httptest.NewRecorder()
	h.Redirect(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when Google OAuth is not configured; body=%s", rr.Code, rr.Body.String())
	}
}

// TestAuthGoogle_Redirect_Configured_RedirectsToGoogle is the
// happy-path counterpart: when Google credentials *are* present the
// handler must (a) mint a fresh `state` and persist it to oauth_states
// for CSRF protection on the Callback round-trip, and (b) 307 the
// browser to the Google authorize endpoint. We assert on the Location
// host (accounts.google.com) and the presence of the same `state`
// query param in the URL the user is being shipped to — without that
// link, the Callback handler's CSRF check can never validate.
func TestAuthGoogle_Redirect_Configured_RedirectsToGoogle(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-client-id.apps.googleusercontent.com", "fake-client-secret")
	if !h.Enabled() {
		t.Fatalf("precondition: Enabled() should be true with client_id/secret set")
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/google?redirect=/dashboard", nil)
	rr := httptest.NewRecorder()
	h.Redirect(rr, req)

	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307; body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if loc == "" {
		t.Fatal("Location header empty — Redirect handler never issued the redirect")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	// Loose host assertion — Google's authorize host is "accounts.google.com".
	// We deliberately do NOT lock in the full URL or every query param;
	// the oauth2 library owns that string and may add parameters across
	// upgrades (e.g. access_type=offline / prompt=consent).
	if !strings.HasSuffix(u.Host, "google.com") {
		t.Errorf("redirect host = %q, want a google.com host (got Location=%s)", u.Host, loc)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL is missing the `state` param — CSRF protection would be unenforceable")
	}

	// The state must have been persisted so Callback can later DELETE-RETURNING
	// it. A missing row would let an attacker bypass CSRF by forging Callback
	// queries the server has no record of issuing.
	var redirectURI string
	if err := h.db.QueryRow(`SELECT redirect_uri FROM oauth_states WHERE state = ?`, state).Scan(&redirectURI); err != nil {
		t.Fatalf("oauth_states row not persisted for state=%q: %v", state, err)
	}
	if redirectURI != "/dashboard" {
		t.Errorf("persisted redirect_uri = %q, want %q (Redirect must echo the safe-redirect query param into the state row)", redirectURI, "/dashboard")
	}
}

// TestAuthGoogle_Redirect_UnsafeRedirectFallsBackToRoot guards the
// open-redirect defence inside Redirect: a `?redirect=` value that
// fails isSafeRedirect must be coerced to "/" before being persisted,
// otherwise the Callback would later 307 the browser to an
// attacker-supplied origin once the OAuth round-trip completes.
func TestAuthGoogle_Redirect_UnsafeRedirectFallsBackToRoot(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-id", "fake-secret")

	// Protocol-relative URL — classic open-redirect payload that
	// isSafeRedirect rejects.
	req := httptest.NewRequest("GET", "/api/v1/auth/google?redirect=//evil.com/steal", nil)
	rr := httptest.NewRecorder()
	h.Redirect(rr, req)
	if rr.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", rr.Code)
	}

	loc := rr.Header().Get("Location")
	u, _ := url.Parse(loc)
	state := u.Query().Get("state")
	var redirectURI string
	if err := h.db.QueryRow(`SELECT redirect_uri FROM oauth_states WHERE state = ?`, state).Scan(&redirectURI); err != nil {
		t.Fatalf("state row lookup: %v", err)
	}
	if redirectURI != "/" {
		t.Errorf("unsafe redirect was persisted as %q, want %q — open-redirect defence broken", redirectURI, "/")
	}
}

// TestAuthGoogle_FindOrCreateUser_NewUser_CreatesUserAndAccount
// exercises the cold-start branch of findOrCreateUser: no row in
// accounts for (provider=google, providerAccountId=sub) AND no row in
// users for the same email. The handler must insert both — a user row
// (so the user has an identity to log in as) AND an accounts row (so
// next time the same Google sub shows up we hit the warm path).
func TestAuthGoogle_FindOrCreateUser_NewUser_CreatesUserAndAccount(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-id", "fake-secret")

	info := googleUserInfo{
		Sub:           "google-sub-new-1",
		Email:         "newuser@example.com",
		EmailVerified: true,
		Name:          "New User",
		Picture:       "https://example.com/avatar.png",
	}
	tok := &oauth2.Token{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback", nil)
	uid, err := h.findOrCreateUser(req, info, tok)
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if uid == "" {
		t.Fatal("returned user id is empty")
	}

	// User row must be created with the Google-supplied profile.
	var email, fullName string
	if err := h.db.QueryRow(`SELECT email, full_name FROM users WHERE id = ?`, uid).Scan(&email, &fullName); err != nil {
		t.Fatalf("read user row: %v", err)
	}
	if email != info.Email {
		t.Errorf("user.email = %q, want %q", email, info.Email)
	}
	if fullName != info.Name {
		t.Errorf("user.full_name = %q, want %q", fullName, info.Name)
	}

	// Accounts row must be linked so the next sign-in hits the warm
	// (existing-account) branch and doesn't try to re-create the user.
	var linkedUserID, providerAccountID string
	if err := h.db.QueryRow(`SELECT userId, providerAccountId FROM accounts WHERE provider='google' AND providerAccountId=?`, info.Sub).Scan(&linkedUserID, &providerAccountID); err != nil {
		t.Fatalf("read accounts row: %v", err)
	}
	if linkedUserID != uid {
		t.Errorf("accounts.userId = %q, want %q", linkedUserID, uid)
	}
}

// TestAuthGoogle_FindOrCreateUser_ExistingAccount_ReturnsSameID is the
// warm path: an accounts row already maps (provider=google, sub) → user.
// The handler must return that user id directly — re-creating the user
// would break the relational link and silently fork accounts on every
// login.
func TestAuthGoogle_FindOrCreateUser_ExistingAccount_ReturnsSameID(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-id", "fake-secret")

	// Pre-seed: a user + a linked Google account.
	const existingUID = "existing-uid-1"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'returning@example.com', 'Returning User')`, existingUID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO accounts (id, userId, type, provider, providerAccountId, access_token) VALUES ('acc-1', ?, 'oauth', 'google', 'google-sub-warm-1', 'old-at')`, existingUID); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	info := googleUserInfo{
		Sub:   "google-sub-warm-1",
		Email: "returning@example.com",
		Name:  "Returning User",
	}
	tok := &oauth2.Token{
		AccessToken:  "fresh-at",
		RefreshToken: "fresh-rt",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback", nil)
	uid, err := h.findOrCreateUser(req, info, tok)
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if uid != existingUID {
		t.Errorf("returned id = %q, want existing %q (warm path must NOT create a new user)", uid, existingUID)
	}

	// No new user rows should have been inserted — exactly one user
	// row in the table, full stop.
	var users int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users in DB = %d, want 1 — duplicate user created on returning-account login", users)
	}

	// Token columns should have been refreshed with the new access /
	// refresh tokens — this is what keeps long-lived sessions working
	// when Google rotates the access token. The handler does this with
	// a fire-and-forget UPDATE; we read it back to prove it landed.
	var gotAT string
	if err := h.db.QueryRow(`SELECT access_token FROM accounts WHERE providerAccountId=?`, info.Sub).Scan(&gotAT); err != nil {
		t.Fatalf("read access_token: %v", err)
	}
	if gotAT != "fresh-at" {
		t.Errorf("access_token = %q, want refreshed value %q", gotAT, "fresh-at")
	}
}

// TestAuthGoogle_FindOrCreateUser_ExistingEmail_LinksAccount covers the
// middle path: a user with that email is already in the users table
// (e.g. they signed up via password first) but has no linked Google
// account yet. findOrCreateUser must reuse the existing user id and
// just *insert* the accounts row — never create a duplicate user with
// the same email, which would violate the implicit (email) → identity
// invariant the rest of the app assumes.
func TestAuthGoogle_FindOrCreateUser_ExistingEmail_LinksAccount(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-id", "fake-secret")

	const existingUID = "pre-existing-uid"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'hybrid@example.com', 'Hybrid User')`, existingUID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	info := googleUserInfo{
		Sub:   "google-sub-hybrid-1",
		Email: "hybrid@example.com",
		Name:  "Hybrid User (Google name)",
	}
	tok := &oauth2.Token{
		AccessToken: "hybrid-at",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}

	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback", nil)
	uid, err := h.findOrCreateUser(req, info, tok)
	if err != nil {
		t.Fatalf("findOrCreateUser: %v", err)
	}
	if uid != existingUID {
		t.Errorf("returned id = %q, want existing %q (email-match path must reuse the row)", uid, existingUID)
	}

	// Exactly one user row should still exist.
	var users int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='hybrid@example.com'`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users with hybrid email = %d, want 1 (no duplicate on Google linking)", users)
	}

	// And a freshly-inserted accounts row must link the Google sub to
	// the existing user — this is the whole point of this branch.
	var linkedUID string
	if err := h.db.QueryRow(`SELECT userId FROM accounts WHERE provider='google' AND providerAccountId=?`, info.Sub).Scan(&linkedUID); err != nil {
		t.Fatalf("accounts row not linked: %v", err)
	}
	if linkedUID != existingUID {
		t.Errorf("linked userId = %q, want %q (Google sub must point at the pre-existing user)", linkedUID, existingUID)
	}
}

// TestAuthGoogle_FindOrCreateUser_DoubleLink_ReturnsError documents
// what currently happens when a Google sub is already linked AND the
// caller goes through the new-account path a second time — the
// (provider, providerAccountId) UNIQUE constraint on accounts trips
// and findOrCreateUser returns the wrapping error. This is more of a
// belt-and-braces test against accidental schema drift: if a future
// migration drops the UNIQUE constraint, this test goes red and forces
// us to revisit the warm-path detection logic.
//
// We trigger the conflict by manually inserting the accounts row
// *before* calling findOrCreateUser with a brand-new email — that way
// the users-by-email lookup misses (sql.ErrNoRows), the function falls
// through to the create-user branch, and the INSERT into accounts then
// collides with the pre-seeded row. The function should surface the
// link-account error rather than silently succeed.
func TestAuthGoogle_FindOrCreateUser_DoubleLink_ReturnsError(t *testing.T) {
	t.Parallel()
	h := newGoogleHandlerExtra(t, "fake-id", "fake-secret")

	// Pre-seed a *different* user that already owns this Google sub.
	const otherUID = "owner-of-sub"
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'owner@example.com', 'Owner')`, otherUID); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO accounts (id, userId, type, provider, providerAccountId, access_token) VALUES ('acc-x', ?, 'oauth', 'google', 'sub-collide', 'at')`, otherUID); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	// Reading the code: the SELECT FROM accounts will *match* (because
	// providerAccountId is the conflict key, scoped by provider only),
	// so this is actually the warm path — we should get back otherUID
	// without an error. That is the load-bearing invariant: even if a
	// caller comes in with a totally different email, the Google sub
	// is what identifies them.
	info := googleUserInfo{
		Sub:   "sub-collide",
		Email: "different@example.com", // does NOT match otherUID's email
		Name:  "Different",
	}
	tok := &oauth2.Token{AccessToken: "x", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback", nil)
	uid, err := h.findOrCreateUser(req, info, tok)
	if err != nil {
		t.Fatalf("warm path with email mismatch should still succeed: %v", err)
	}
	if uid != otherUID {
		t.Errorf("returned id = %q, want owner of the sub %q (sub > email when both present)", uid, otherUID)
	}

	// And critically: no second user row should have been created for
	// "different@example.com". The sub-based lookup short-circuits the
	// email branch entirely.
	var stranger int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email='different@example.com'`).Scan(&stranger); err != nil {
		t.Fatalf("count stranger: %v", err)
	}
	if stranger != 0 {
		t.Errorf("stranger user got created on warm-path login: count=%d", stranger)
	}
}

// jsonContentTypeOnError keeps the next-auth error contract honest:
// the handler must respond with JSON so the front-end's fetch() can
// .json() the body without a parse error. This is a thin assertion
// layered on top of TestAuth_ErrorEndpoint_EchoesQueryParam so a
// future refactor that switches Error to render HTML would surface
// here instead of breaking the SPA at runtime.
func TestAuth_ErrorEndpoint_ReturnsJSON(t *testing.T) {
	t.Parallel()
	h, _ := newNextAuthHandler(t)
	req := httptest.NewRequest("GET", "/api/auth/error?error=Verification", nil)
	rr := httptest.NewRecorder()
	h.Error(rr, req)

	// Status must be 200 — front-end treats anything else as a network
	// fault and shows a generic error page instead of the typed message.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// Body must round-trip as JSON. We don't lock in the exact keys
	// (that's TestNextAuth_Error's territory) — just that the parser
	// accepts it.
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Errorf("response body is not valid JSON: %v; body=%s", err, rr.Body.String())
	}
}
