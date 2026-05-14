package api

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Path-scope on the refresh cookie keeps the long-lived token out of
// the bulk of the API surface. Only /api/auth/* paths ever receive it
// — that's still the entire NextAuth-compat surface (signin, signout,
// session, csrf, refresh) but excludes /api/v1/* and the WebSocket,
// where a leaked refresh cookie would be most damaging. The slightly
// looser scope (vs. just /api/auth/token/refresh) is what makes
// signOut able to revoke the user_sessions row even when the access
// cookie has expired — without it, the only carrier of session_id
// would be a 15-min-expired access token, leaving a stale row in the
// "Active sessions" list for 30 days after the user idle-logged-out.
const refreshCookiePath = "/api/auth/"

// NextAuthHandler implements the endpoints that next-auth/react client SDK expects.
// This allows the static-exported Next.js frontend to use signIn(), signOut(), useSession().
type NextAuthHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	validator *auth.JWTValidator
	sessions  sessions.Store
}

// NewNextAuthHandler creates a NextAuthHandler for compatibility with the next-auth client SDK.
// sessionsStore must back user_sessions (migration v63); pass *sessions.DBStore in production.
func NewNextAuthHandler(db *sql.DB, logger *slog.Logger, validator *auth.JWTValidator, sessionsStore sessions.Store) *NextAuthHandler {
	return &NextAuthHandler{db: db, logger: logger, validator: validator, sessions: sessionsStore}
}

func (h *NextAuthHandler) csrfCookieName(r *http.Request) string {
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		return "__Host-authjs.csrf-token"
	}
	return "authjs.csrf-token"
}

func (h *NextAuthHandler) csrfToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("csrfToken: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// sessionCookieName resolves the access-token cookie name for r,
// prefixed with __Secure- when the request is HTTPS so the browser
// enforces the secure-context contract.
func sessionCookieName(r *http.Request) string {
	if isHTTPS(r) {
		return "__Secure-authjs.session-token"
	}
	return "authjs.session-token"
}

// refreshCookieName resolves the refresh-token cookie name for r.
// Pair with refreshCookiePath when issuing — keeps the cookie scoped
// to the refresh endpoint instead of leaking onto every request.
func refreshCookieName(r *http.Request) string {
	if isHTTPS(r) {
		return "__Secure-authjs.refresh-token"
	}
	return "authjs.refresh-token"
}

// setAuthCookies writes both the access + refresh cookies in the
// canonical NextAuth-compatible shape: HttpOnly, SameSite=Lax, Secure
// auto-toggled by TLS, __Secure- prefix over HTTPS, refresh path-scoped
// to refreshCookiePath so it never reaches non-auth handlers.
//
// All three auth entry points (email/password, Google OAuth, the
// NextAuth bridge) used to inline this shape; centralizing it here
// means a flag change lands in one place and the three paths can't
// drift. Callers that want non-default TTLs should construct the
// cookies themselves rather than asking this helper to grow knobs.
func setAuthCookies(w http.ResponseWriter, r *http.Request, access, refresh string) {
	secure := isHTTPS(r)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(r),
		Value:    access,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.AccessTokenTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName(r),
		Value:    refresh,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.RefreshTokenTTL.Seconds()),
	})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// CSRF returns a CSRF token (GET /api/auth/csrf)
func (h *NextAuthHandler) CSRF(w http.ResponseWriter, r *http.Request) {
	token, err := h.csrfToken()
	if err != nil {
		h.logger.Error("generate csrf token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	cookieName := h.csrfCookieName(r)
	isSecure := strings.HasPrefix(cookieName, "__Host-")
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrfToken": token})
}

// Providers returns available auth providers (GET /api/auth/providers)
func (h *NextAuthHandler) Providers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"credentials": map[string]interface{}{
			"id":          "credentials",
			"name":        "Credentials",
			"type":        "credentials",
			"signinUrl":   "/api/auth/callback/credentials",
			"callbackUrl": "/api/auth/callback/credentials",
		},
	})
}

// Session returns the current session (GET /api/auth/session). Returns
// an empty object when no valid access cookie is present — this is what
// next-auth/react interprets as "unauthenticated" without showing an
// error. The cookie is also dropped on validation failure so a stale
// token doesn't keep getting sent until 30 days from now.
func (h *NextAuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	cookieName := sessionCookieName(r)
	cookie, err := r.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}

	claims, err := h.validator.ValidateAccess(cookie.Value)
	if err != nil {
		h.clearAuthCookies(w, r)
		writeJSON(w, http.StatusOK, map[string]interface{}{})
		return
	}

	// Session row may have been revoked between this request and the
	// last refresh. Treat that as logged-out, same as missing cookie.
	//
	// But a transient sessions.Get failure must NOT clear cookies —
	// that's the same false-logout trap the middleware fixed: a DB
	// hiccup would evict the user permanently. Only ErrNotFound (row
	// gone) or sess.Active==false (revoked/expired) clear the cookies.
	// On other errors return 500 and leave the cookies intact so the
	// next /api/auth/session call after recovery proceeds normally.
	if h.sessions != nil && claims.Sid != "" {
		sess, err := h.sessions.Get(r.Context(), claims.Sid)
		switch {
		case errors.Is(err, sessions.ErrNotFound):
			h.clearAuthCookies(w, r)
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		case err != nil:
			h.logger.Error("session lookup in /auth/session", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		case !sess.Active(time.Now()):
			h.clearAuthCookies(w, r)
			writeJSON(w, http.StatusOK, map[string]interface{}{})
			return
		}
	}

	expires := time.Unix(claims.Exp, 0).UTC().Format(time.RFC3339)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"user": map[string]interface{}{
			"id":    claims.ID,
			"name":  claims.Name,
			"email": claims.Email,
		},
		"expires": expires,
	})
}

// CallbackCredentials handles login (POST /api/auth/callback/credentials)
func (h *NextAuthHandler) CallbackCredentials(w http.ResponseWriter, r *http.Request) {
	csrfCookie, _ := r.Cookie(h.csrfCookieName(r))
	if csrfCookie == nil || csrfCookie.Value == "" {
		replyError(w, http.StatusForbidden, "Missing CSRF token")
		return
	}

	isJSON := strings.Contains(r.Header.Get("Content-Type"), "json")

	var email, password, csrfToken string
	if isJSON {
		var body map[string]interface{}
		if err := readJSON(r, &body); err != nil {
			replyError(w, http.StatusBadRequest, "Invalid request")
			return
		}
		if v, ok := body["email"].(string); ok {
			email = v
		}
		if v, ok := body["password"].(string); ok {
			password = v
		}
		if v, ok := body["csrfToken"].(string); ok {
			csrfToken = v
		}
	} else {
		r.ParseForm()
		email = r.FormValue("email")
		password = r.FormValue("password")
		csrfToken = r.FormValue("csrfToken")
	}

	wantJSON := isJSON ||
		r.FormValue("json") == "true" ||
		r.FormValue("redirect") == "false"

	if subtle.ConstantTimeCompare([]byte(csrfToken), []byte(csrfCookie.Value)) != 1 {
		replyError(w, http.StatusForbidden, "Invalid CSRF token")
		return
	}

	if email == "" || password == "" {
		h.respondCredentialsError(w, r, wantJSON)
		return
	}

	// Lockout-aware credentials check. Distinguishes locked-account
	// from invalid-credentials internally for logging, but the wire
	// response is the same generic CredentialsSignin so an attacker
	// can't use the response to enumerate which emails exist or
	// which are currently locked.
	userID, fullName, err := checkAndLockoutOnFail(r.Context(), h.db, email, password, time.Now())
	if err != nil {
		if errors.Is(err, ErrAccountLocked) {
			h.logger.Warn("login blocked by lockout",
				"email", email, "ip", clientIP(r))
		} else if !errors.Is(err, ErrInvalidCredentials) {
			h.logger.Error("login lockout check", "error", err, "email", email)
		}
		h.respondCredentialsError(w, r, wantJSON)
		return
	}

	// Mint a fresh user_sessions row + matching access/refresh cookies.
	// Failures here are 500 — the user authenticated successfully so
	// "internal error" is the truthful response.
	sess, accessTok, refreshTok, err := h.issueSession(r, userID, fullName, email)
	if err != nil {
		h.logger.Error("issue session", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	setAuthCookies(w, r, accessTok, refreshTok)
	_ = sess

	callbackUrl := r.FormValue("callbackUrl")
	if callbackUrl == "" {
		callbackUrl = "/"
	}
	// V-06: Prevent open redirect — only allow relative paths
	if !strings.HasPrefix(callbackUrl, "/") || strings.HasPrefix(callbackUrl, "//") {
		callbackUrl = "/"
	}

	if wantJSON {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":     true,
			"url":    callbackUrl,
			"status": 200,
		})
	} else {
		http.Redirect(w, r, callbackUrl, http.StatusFound)
	}
}

// SignOut handles logout (POST /api/auth/signout). Revokes the session
// row server-side AND clears both cookies — once a logged-out cookie
// is somehow replayed it'll fail at the middleware's session lookup
// with reasonSessionRevoked, not just be absent.
//
// Looks at BOTH the access and refresh cookie to find the session id:
// if the access token has already expired (15 min default), the user
// can still hit signOut (browsers send refresh cookie too — the path
// scope only restricts where it's sent, /api/auth covers the refresh
// AND signout endpoint paths). Without the refresh-fallback, a tab
// that idled past access expiry would clear cookies but leave a stale
// active row in user_sessions, polluting the "Active sessions" list.
func (h *NextAuthHandler) SignOut(w http.ResponseWriter, r *http.Request) {
	sid := h.findSessionID(r)
	if sid != "" && h.sessions != nil {
		if err := h.sessions.Revoke(r.Context(), sid, sessions.ReasonLogout); err != nil && !errors.Is(err, sessions.ErrNotFound) {
			h.logger.Warn("signout revoke failed", "sid", sid, "error", err)
		}
	}

	h.clearAuthCookies(w, r)

	isJSON := strings.Contains(r.Header.Get("Accept"), "json") ||
		strings.Contains(r.Header.Get("Content-Type"), "json")

	if isJSON || r.Method == "POST" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"url": "/login",
		})
	} else {
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// SignIn redirects to the login page (GET /api/auth/signin)
func (h *NextAuthHandler) SignIn(w http.ResponseWriter, r *http.Request) {
	callbackUrl := r.URL.Query().Get("callbackUrl")
	if callbackUrl == "" {
		callbackUrl = "/"
	}
	// V-06: Prevent open redirect — only allow relative paths
	if !strings.HasPrefix(callbackUrl, "/") || strings.HasPrefix(callbackUrl, "//") {
		callbackUrl = "/"
	}
	http.Redirect(w, r, "/login?callbackUrl="+url.QueryEscape(callbackUrl), http.StatusFound)
}

// Error shows auth error (GET /api/auth/error)
func (h *NextAuthHandler) Error(w http.ResponseWriter, r *http.Request) {
	errType := r.URL.Query().Get("error")
	if errType == "" {
		errType = "Default"
	}
	msg := fmt.Sprintf("Authentication error: %s", errType)
	writeJSON(w, http.StatusOK, map[string]string{"error": errType, "message": msg})
}

// RefreshToken handles POST /api/auth/token/refresh.
//
// CSRF defense (layered):
//   - Path-scoped refresh cookie + SameSite=Lax — cross-site POST
//     never sends the cookie at all (browser policy).
//   - POST-only — GET-embed CSRF can't reach this handler.
//   - Origin/Referer same-origin check below — defence-in-depth.
//
// Token-theft defense — refresh-token rotation with reuse detection
// (OWASP ASVS V7.4.4 / V3.7.4):
//   - Each refresh token has a unique JTI.
//   - The session row tracks current_refresh_jti.
//   - Successful refresh CAS-rotates: old jti → new jti.
//   - A request carrying an old jti (i.e. one we've already rotated
//     past) is the theft signal: revoke the entire session and 401.
//
// On any failure both cookies are cleared so the client doesn't keep
// resending dead tokens.
func (h *NextAuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		replyError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}

	if !h.sameOriginRefresh(r) {
		writeAuthError(w, http.StatusForbidden, reasonSessionInvalid)
		return
	}

	cookie, err := r.Cookie(refreshCookieName(r))
	if err != nil || cookie.Value == "" {
		// Accept the legacy cookie name for one release cycle so users
		// don't get bounced mid-session by the deploy. Migration to
		// the new path-scoped cookie happens on next signIn.
		if alt, altErr := r.Cookie("authjs.refresh-token"); altErr == nil && alt.Value != "" {
			cookie = alt
		} else {
			writeAuthError(w, http.StatusUnauthorized, reasonSessionExpired)
			return
		}
	}

	claims, err := h.validator.ValidateRefresh(cookie.Value)
	if err != nil {
		h.clearAuthCookies(w, r)
		writeAuthError(w, http.StatusUnauthorized, reasonSessionExpired)
		return
	}

	if h.sessions == nil {
		h.logger.Error("refresh: sessions store not configured")
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Distinguish "session is gone" (clear cookies, 401) from
	// "store is temporarily unreachable" (preserve cookies, 500).
	// The frontend's tryRefresh treats 5xx as retryable_failed and
	// won't fire session-expired — see lib/api-fetch.ts. Without
	// this split, every transient DB blip on the refresh endpoint
	// would evict every authenticated user.
	sess, err := h.sessions.Get(r.Context(), claims.Sid)
	switch {
	case errors.Is(err, sessions.ErrNotFound):
		h.clearAuthCookies(w, r)
		writeAuthError(w, http.StatusUnauthorized, reasonSessionRevoked)
		return
	case err != nil:
		h.logger.Error("refresh: session lookup", "error", err, "sid", claims.Sid)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	case !sess.Active(time.Now()):
		h.clearAuthCookies(w, r)
		reason := reasonSessionExpired
		if sess.RevokedAt != nil {
			reason = reasonSessionRevoked
		}
		writeAuthError(w, http.StatusUnauthorized, reason)
		return
	}

	// Look up name/email to bake into the new access token. If the
	// user was deleted out from under us, the JOIN-fk in user_sessions
	// would have CASCADE-deleted the session — but defend anyway.
	var fullName, email string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT full_name, email FROM users WHERE id = ?", claims.ID,
	).Scan(&fullName, &email); err != nil {
		h.clearAuthCookies(w, r)
		_ = h.sessions.Revoke(r.Context(), claims.Sid, sessions.ReasonAdminForce)
		writeAuthError(w, http.StatusUnauthorized, reasonSessionRevoked)
		return
	}

	// Mint the new tokens FIRST so we have the new refresh JTI to
	// CAS-rotate against. If anything below fails after this point we
	// have not yet committed the rotation, so the original cookie is
	// still valid and the client can simply retry.
	access, err := h.validator.IssueAccessToken(claims.ID, claims.Sid, fullName, email)
	if err != nil {
		h.logger.Error("issue access on refresh", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	refresh, err := h.validator.IssueRefreshToken(claims.ID, claims.Sid)
	if err != nil {
		h.logger.Error("issue refresh on refresh", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	newClaims, err := h.validator.ValidateRefresh(refresh)
	if err != nil {
		h.logger.Error("validate freshly-issued refresh", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// CAS rotate. Mismatch == replay attempt — somebody else used a
	// previous refresh in this chain and we already rotated past
	// claims.Jti. Revoke the entire session: the rightful client is
	// going to find their next request 401'd, which is correct given
	// we cannot tell rightful from impostor at this point.
	if err := h.sessions.RotateRefreshJti(r.Context(), claims.Sid, claims.Jti, newClaims.Jti); err != nil {
		h.clearAuthCookies(w, r)
		if errors.Is(err, sessions.ErrJTIMismatch) {
			h.logger.Warn("refresh token replay detected — revoking session",
				"sid", claims.Sid, "user_id", claims.ID, "ip", clientIP(r))
			_ = h.sessions.Revoke(r.Context(), claims.Sid, sessions.ReasonAdminForce)
			writeAuthError(w, http.StatusUnauthorized, reasonSessionRevoked)
			return
		}
		if errors.Is(err, sessions.ErrNotFound) {
			writeAuthError(w, http.StatusUnauthorized, reasonSessionExpired)
			return
		}
		h.logger.Error("rotate refresh jti", "error", err, "sid", claims.Sid)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	setAuthCookies(w, r, access, refresh)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"expires": time.Now().Add(auth.AccessTokenTTL).UTC().Format(time.RFC3339),
	})
}

// sameOriginRefresh enforces that POST /api/auth/token/refresh comes
// from a page hosted on the same origin as the request. Fetch always
// sends an Origin header on cross-origin requests; same-origin POSTs
// either omit Origin (older Safari) or set it to the page origin.
// We require either no Origin, or Origin matching the request Host.
//
// Defense-in-depth on top of the SameSite=Lax cookie + Path-scope.
// Returns true if the request looks legitimate or if both Origin and
// Referer are absent (curl, mobile native clients) — those still need
// the refresh cookie which they wouldn't have unless they obtained it
// through a same-origin signIn.
func (h *NextAuthHandler) sameOriginRefresh(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		// Strip port for comparison; dev-server proxies and SSH
		// tunnels frequently swap the port.
		oh := u.Hostname()
		rh := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			rh = h
		}
		return oh == rh
	}
	// No Origin header: also accept Referer same-host. Some Safari
	// versions strip Origin on same-origin same-method POSTs.
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil {
			return false
		}
		oh := u.Hostname()
		rh := host
		if h, _, err := net.SplitHostPort(host); err == nil {
			rh = h
		}
		return oh == rh
	}
	// Neither header present — accept; the cookie still has to be
	// valid which is the harder gate.
	return true
}

// issueSession is the shared signin path used by credentials, OAuth
// callbacks, and the signup handler. It writes the user_sessions row
// and produces both tokens; the caller is responsible for setting
// the cookies on the response.
//
// If token issuance fails after the row has been created, we revoke
// the row before returning the error. Without this rollback, every
// validator failure (e.g. a transient JWE encrypt error) would leave
// an active row that the user never received cookies for — a ghost
// "device" in the Active sessions UI and a slow leak that pollutes
// audit and policy state. Revoke errors are logged and swallowed
// because we're already on a failure path; the original mint error
// is what we want to surface.
func (h *NextAuthHandler) issueSession(r *http.Request, userID, name, email string) (*sessions.Session, string, string, error) {
	if h.sessions == nil {
		return nil, "", "", errors.New("sessions store not configured")
	}
	sess, err := h.sessions.Create(r.Context(), userID, r.UserAgent(), clientIP(r), auth.RefreshTokenTTL)
	if err != nil {
		return nil, "", "", fmt.Errorf("create session: %w", err)
	}
	access, err := h.validator.IssueAccessToken(userID, sess.ID, name, email)
	if err != nil {
		if rerr := h.sessions.Revoke(r.Context(), sess.ID, sessions.ReasonAdminForce); rerr != nil && !errors.Is(rerr, sessions.ErrNotFound) {
			h.logger.Warn("revoke ghost session after issue access failure", "sid", sess.ID, "error", rerr)
		}
		return nil, "", "", fmt.Errorf("issue access: %w", err)
	}
	refresh, err := h.validator.IssueRefreshToken(userID, sess.ID)
	if err != nil {
		if rerr := h.sessions.Revoke(r.Context(), sess.ID, sessions.ReasonAdminForce); rerr != nil && !errors.Is(rerr, sessions.ErrNotFound) {
			h.logger.Warn("revoke ghost session after issue refresh failure", "sid", sess.ID, "error", rerr)
		}
		return nil, "", "", fmt.Errorf("issue refresh: %w", err)
	}
	return sess, access, refresh, nil
}


// clearAuthCookies expires both cookies. Called on signOut, refresh
// failure, and detected stale cookies on /api/auth/session — the
// browser shouldn't keep sending dead tokens for 30 days.
func (h *NextAuthHandler) clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	for _, c := range []struct {
		name string
		path string
	}{
		{sessionCookieName(r), "/"},
		{"authjs.session-token", "/"},
		{"__Secure-authjs.session-token", "/"},
		{refreshCookieName(r), refreshCookiePath},
		{"authjs.refresh-token", refreshCookiePath},
		{"__Secure-authjs.refresh-token", refreshCookiePath},
	} {
		http.SetCookie(w, &http.Cookie{
			Name:     c.name,
			Value:    "",
			Path:     c.path,
			HttpOnly: true,
			Secure:   isHTTPS(r),
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})
	}
}

func (h *NextAuthHandler) respondCredentialsError(w http.ResponseWriter, r *http.Request, wantJSON bool) {
	if wantJSON {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"error": "CredentialsSignin",
			"ok":    false,
			"url":   "/api/auth/error?error=CredentialsSignin",
		})
	} else {
		http.Redirect(w, r, "/login?error=CredentialsSignin", http.StatusFound)
	}
}

// findSessionID returns the user_sessions.id derivable from whichever
// cookie the client still has. Tries access first (cheaper, no DB look-
// ahead needed by callers), then refresh as a fallback. Returns "" when
// neither cookie is present, decoded successfully, or carried a sid.
//
// Used by signOut, but suitable for any handler that needs to operate
// on "the caller's session" without depending on access-cookie freshness.
func (h *NextAuthHandler) findSessionID(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookieName(r)); err == nil && cookie.Value != "" {
		if claims, err := h.validator.ValidateAccess(cookie.Value); err == nil && claims.Sid != "" {
			return claims.Sid
		}
	}
	// Try both the secure and non-secure name; we may get either at
	// signout time (HTTPS upgrade, proxy quirks, downgrade attacks).
	for _, name := range []string{refreshCookieName(r), "authjs.refresh-token", "__Secure-authjs.refresh-token"} {
		if cookie, err := r.Cookie(name); err == nil && cookie.Value != "" {
			if claims, err := h.validator.ValidateRefresh(cookie.Value); err == nil && claims.Sid != "" {
				return claims.Sid
			}
		}
	}
	return ""
}

// clientIP extracts a best-effort caller IP. X-Forwarded-For wins when
// present (we trust it because the only proxies in the path are our
// own dev-server and the production Go binary), falling back to the
// raw RemoteAddr. Used only for the audit column user_sessions.ip.
//
// IPv6 quirk: r.RemoteAddr is "[::1]:8080" and the previous LastIndexByte
// approach truncated to "[::1" — the audit row would then have a
// malformed address and a future "show me sessions from this IP" query
// wouldn't match. net.SplitHostPort handles bracketed IPv6 correctly;
// when it errors (no port present, malformed input), we return the
// original string rather than empty so a misformatted address is
// preserved rather than silently dropped.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if r.RemoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
