package api

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/crypto/bcrypt"
)

// Path-scope on the refresh cookie keeps the long-lived token out of
// every other endpoint's request. Only /api/auth/token/refresh ever
// receives it; a stolen access cookie can't be turned into a fresh
// chain unless the attacker also pulled the refresh cookie, which
// browser policy stops them from sending elsewhere.
const refreshCookiePath = "/api/auth/token/refresh"

// NextAuthHandler implements the endpoints that next-auth/react client SDK expects.
// This allows the static-exported Next.js frontend to use signIn(), signOut(), useSession().
type NextAuthHandler struct {
	db        *sql.DB
	logger    *slog.Logger
	validator *auth.JWTValidator
	sessions  sessions.Store
}

// NewNextAuthHandler creates a NextAuthHandler for compatibility with the next-auth client SDK.
// sessionsStore must back user_sessions (migration v60); pass *sessions.DBStore in production.
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

func (h *NextAuthHandler) sessionCookieName(r *http.Request) string {
	if isHTTPS(r) {
		return "__Secure-authjs.session-token"
	}
	return "authjs.session-token"
}

func (h *NextAuthHandler) refreshCookieName(r *http.Request) string {
	if isHTTPS(r) {
		return "__Secure-authjs.refresh-token"
	}
	return "authjs.refresh-token"
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// CSRF returns a CSRF token (GET /api/auth/csrf)
func (h *NextAuthHandler) CSRF(w http.ResponseWriter, r *http.Request) {
	token, err := h.csrfToken()
	if err != nil {
		h.logger.Error("generate csrf token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
	cookieName := h.sessionCookieName(r)
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
	if h.sessions != nil && claims.Sid != "" {
		sess, err := h.sessions.Get(r.Context(), claims.Sid)
		if err != nil || !sess.Active(time.Now()) {
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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Missing CSRF token"})
		return
	}

	isJSON := strings.Contains(r.Header.Get("Content-Type"), "json")

	var email, password, csrfToken string
	if isJSON {
		var body map[string]interface{}
		if err := readJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
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
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Invalid CSRF token"})
		return
	}

	if email == "" || password == "" {
		h.respondCredentialsError(w, r, wantJSON)
		return
	}

	var userID, fullName, hashedPw string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id, full_name, hashed_password FROM users WHERE email = ?", email,
	).Scan(&userID, &fullName, &hashedPw)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hashedPw), []byte(password)) != nil {
		h.respondCredentialsError(w, r, wantJSON)
		return
	}

	// Mint a fresh user_sessions row + matching access/refresh cookies.
	// Failures here are 500 — the user authenticated successfully so
	// "internal error" is the truthful response.
	sess, accessTok, refreshTok, err := h.issueSession(r, userID, fullName, email)
	if err != nil {
		h.logger.Error("issue session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	h.setAuthCookies(w, r, accessTok, refreshTok)
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
func (h *NextAuthHandler) SignOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(h.sessionCookieName(r)); err == nil && cookie.Value != "" {
		if claims, vErr := h.validator.ValidateAccess(cookie.Value); vErr == nil && claims.Sid != "" && h.sessions != nil {
			if err := h.sessions.Revoke(r.Context(), claims.Sid, sessions.ReasonLogout); err != nil && !errors.Is(err, sessions.ErrNotFound) {
				h.logger.Warn("signout revoke failed", "sid", claims.Sid, "error", err)
			}
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

// RefreshToken handles POST /api/auth/token/refresh. The browser is
// the only legitimate caller — Path-scoped refresh cookie + SameSite=Lax
// is the CSRF defense. We additionally require the request to be a POST
// so a CSRF GET embed can't trigger a rotation.
//
// On success: rotate the refresh token (old one is revoked via session
// rotation, new one minted) and re-issue access. On failure: clear both
// cookies and respond 401 session_expired so the client redirects.
func (h *NextAuthHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	cookie, err := r.Cookie(h.refreshCookieName(r))
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	sess, err := h.sessions.Get(r.Context(), claims.Sid)
	if err != nil || !sess.Active(time.Now()) {
		h.clearAuthCookies(w, r)
		reason := reasonSessionExpired
		if err == nil && sess.RevokedAt != nil {
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

	access, err := h.validator.IssueAccessToken(claims.ID, claims.Sid, fullName, email)
	if err != nil {
		h.logger.Error("issue access on refresh", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	refresh, err := h.validator.IssueRefreshToken(claims.ID, claims.Sid)
	if err != nil {
		h.logger.Error("issue refresh on refresh", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.setAuthCookies(w, r, access, refresh)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"expires": time.Now().Add(auth.AccessTokenTTL).UTC().Format(time.RFC3339),
	})
}

// issueSession is the shared signin path used by credentials, OAuth
// callbacks, and the signup handler. It writes the user_sessions row
// and produces both tokens; the caller is responsible for setting
// the cookies on the response.
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
		return nil, "", "", fmt.Errorf("issue access: %w", err)
	}
	refresh, err := h.validator.IssueRefreshToken(userID, sess.ID)
	if err != nil {
		return nil, "", "", fmt.Errorf("issue refresh: %w", err)
	}
	return sess, access, refresh, nil
}

// setAuthCookies writes both auth cookies. Access is Path=/ so every
// API endpoint can read it; refresh is Path-scoped to the refresh
// endpoint so it never leaks to other handlers.
func (h *NextAuthHandler) setAuthCookies(w http.ResponseWriter, r *http.Request, access, refresh string) {
	accessName := h.sessionCookieName(r)
	refreshName := h.refreshCookieName(r)
	secure := isHTTPS(r)

	http.SetCookie(w, &http.Cookie{
		Name:     accessName,
		Value:    access,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.AccessTokenTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     refreshName,
		Value:    refresh,
		Path:     refreshCookiePath,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(auth.RefreshTokenTTL.Seconds()),
	})
}

// clearAuthCookies expires both cookies. Called on signOut, refresh
// failure, and detected stale cookies on /api/auth/session — the
// browser shouldn't keep sending dead tokens for 30 days.
func (h *NextAuthHandler) clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	for _, c := range []struct {
		name string
		path string
	}{
		{h.sessionCookieName(r), "/"},
		{"authjs.session-token", "/"},
		{"__Secure-authjs.session-token", "/"},
		{h.refreshCookieName(r), refreshCookiePath},
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

// clientIP extracts a best-effort caller IP. X-Forwarded-For wins when
// present (we trust it because the only proxies in the path are our
// own dev-server and the production Go binary), falling back to the
// raw RemoteAddr. Used only for the audit column user_sessions.ip.
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
	if i := strings.LastIndexByte(r.RemoteAddr, ':'); i >= 0 {
		return r.RemoteAddr[:i]
	}
	return r.RemoteAddr
}
