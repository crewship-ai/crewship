package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// timeNow is wrapped so tests can advance the middleware's clock — for
// example to verify a session is rejected at exactly the expires_at
// boundary. Production code never reassigns it.
var timeNow = time.Now

type contextKey string

const (
	ctxUser        contextKey = "user"
	ctxWorkspaceID contextKey = "workspace_id"
	ctxRole        contextKey = "role"
)

// Reason codes returned in 401 bodies and WWW-Authenticate. The
// frontend's apiFetch wrapper inspects the body to decide between
// "try refresh" (session_expired) and "give up" (session_revoked /
// session_invalid). Don't add new values without updating that branch.
const (
	reasonSessionExpired = "session_expired"
	reasonSessionRevoked = "session_revoked"
	reasonSessionInvalid = "session_invalid"
	reasonNoCredentials  = "no_credentials"
)

// AuthUser represents an authenticated user extracted from a JWT or CLI token.
// SessionID is empty for CLI-token auth (those don't have user_sessions rows)
// and populated for JWT auth — handlers that need to revoke or rotate the
// caller's session (signOut, refresh) read it from here.
type AuthUser struct {
	ID        string
	Email     string
	Name      string
	SessionID string
}

// UserFromContext returns the authenticated user stored in the request context, or nil if not set.
func UserFromContext(ctx context.Context) *AuthUser {
	u, _ := ctx.Value(ctxUser).(*AuthUser)
	return u
}

// WorkspaceIDFromContext returns the workspace ID stored in the request context, or empty string if not set.
func WorkspaceIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxWorkspaceID).(string)
	return s
}

// RoleFromContext returns the workspace membership role (e.g. OWNER, ADMIN, MEMBER) from the request context.
func RoleFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxRole).(string)
	return s
}

// SecurityHeaders is middleware that sets standard security response headers.
// It does NOT set HSTS (the binary may run on plain HTTP) or CSP (needs separate analysis).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XSS-Protection", "0")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware provides HTTP middleware for JWT and CLI token authentication.
// The sessions store is consulted on every JWT-auth request to enforce
// server-side revocation; pass a no-op store only in tests that exercise
// pure CLI-token paths.
type AuthMiddleware struct {
	validator *auth.JWTValidator
	sessions  sessions.Store
	db        *sql.DB
	logger    *slog.Logger
}

// NewAuthMiddleware creates an AuthMiddleware. sessionsStore must back
// the user_sessions table (migration v60); pass *sessions.DBStore in
// production.
func NewAuthMiddleware(validator *auth.JWTValidator, sessionsStore sessions.Store, db *sql.DB, logger *slog.Logger) *AuthMiddleware {
	return &AuthMiddleware{validator: validator, sessions: sessionsStore, db: db, logger: logger}
}

// RequireAuth returns middleware that validates the request's Bearer token or CLI token
// and stores the authenticated user in the request context.
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized, reasonNoCredentials)
			return
		}

		var user *AuthUser

		if IsCLIToken(token) {
			userID, email, name, err := ValidateCLIToken(m.db, token)
			if err != nil {
				m.logger.Debug("CLI token auth failed", "error", err)
				writeAuthError(w, http.StatusUnauthorized, reasonSessionInvalid)
				return
			}
			user = &AuthUser{ID: userID, Email: email, Name: name}
		} else {
			claims, err := m.validator.ValidateAccess(token)
			if err != nil {
				m.logger.Debug("auth failed", "error", err)
				writeAuthError(w, http.StatusUnauthorized, reasonForJWTErr(err))
				return
			}

			// Tokens MUST carry a session id. Empty sid means either
			// (a) a token minted before migration v60 ever ran, or
			// (b) a hand-crafted token from a non-issuer code path.
			// Either way it bypasses the user_sessions revocation
			// check — i.e. signOut, password-change, admin force-
			// logout would all fail to invalidate it. Refuse it.
			if claims.Sid == "" {
				m.logger.Warn("rejected access token without sid",
					"user_id", claims.ID, "jti", claims.Jti)
				writeAuthError(w, http.StatusUnauthorized, reasonSessionInvalid)
				return
			}

			// JWT signed by us and not yet expired — but the session
			// may have been revoked since this token was minted.
			// user_sessions is the source of truth for "is this
			// session still valid"; the JWT exp is just an upper
			// bound on revocation latency.
			//
			// Critically: a transient store failure (DB timeout,
			// momentary unavailability) MUST NOT 401 the user.
			// 401 makes the frontend's apiFetch try refresh, fail
			// the same way, and bounce to /login — every backend
			// hiccup turns into a forced logout. We return 500
			// instead so the access cookie survives until the
			// store is reachable again.
			if m.sessions == nil {
				m.logger.Error("auth middleware: sessions store not configured")
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
			sess, err := m.sessions.Get(r.Context(), claims.Sid)
			if err != nil {
				if errors.Is(err, sessions.ErrNotFound) {
					writeAuthError(w, http.StatusUnauthorized, reasonSessionRevoked)
					return
				}
				m.logger.Error("session lookup failed", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
			if !sess.Active(timeNow()) {
				reason := reasonSessionRevoked
				if sess.RevokedAt == nil {
					reason = reasonSessionExpired
				}
				writeAuthError(w, http.StatusUnauthorized, reason)
				return
			}
			// Best-effort touch — failures here are logged but
			// don't block the request. The whole point of the
			// throttle is that a transient SQLite error on one
			// touch shouldn't 500 a happy-path API call.
			if err := m.sessions.TouchLastUsed(r.Context(), claims.Sid); err != nil {
				m.logger.Debug("touch last_used failed", "error", err)
			}

			user = &AuthUser{
				ID:        claims.ID,
				Email:     claims.Email,
				Name:      claims.Name,
				SessionID: claims.Sid,
			}
		}

		ctx := context.WithValue(r.Context(), ctxUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireWorkspace returns middleware that verifies the authenticated user is a member
// of the requested workspace and stores the workspace ID and role in the context.
func (m *AuthMiddleware) RequireWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := UserFromContext(r.Context())
		if user == nil {
			writeAuthError(w, http.StatusUnauthorized, reasonNoCredentials)
			return
		}

		workspaceID := r.URL.Query().Get("workspace_id")
		if workspaceID == "" {
			workspaceID = r.PathValue("workspaceId")
		}
		if workspaceID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
			return
		}

		var role string
		err := m.db.QueryRowContext(r.Context(),
			"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
			workspaceID, user.ID,
		).Scan(&role)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Not a member of this workspace"})
			return
		}

		ctx := context.WithValue(r.Context(), ctxWorkspaceID, workspaceID)
		ctx = context.WithValue(ctx, ctxRole, role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}

	for _, name := range []string{"authjs.session-token", "__Secure-authjs.session-token"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}

	return ""
}

// writeAuthError writes a 401 JSON body together with a WWW-Authenticate
// header carrying the same reason code, so XHR clients can read it from
// either side. The header form follows RFC 6750 §3.
func writeAuthError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="%s"`, reason))
	writeJSON(w, status, map[string]string{"error": reason})
}

// reasonForJWTErr maps a JWT validator error into the wire reason code
// the frontend's apiFetch wrapper expects.
func reasonForJWTErr(err error) string {
	switch {
	case errors.Is(err, auth.ErrTokenExpired):
		return reasonSessionExpired
	case errors.Is(err, auth.ErrInvalidToken), errors.Is(err, auth.ErrWrongKind):
		return reasonSessionInvalid
	default:
		return reasonSessionInvalid
	}
}

// internalWsCtx extracts workspace_id from query params and sets it in context.
// Used for internal routes called by sidecar (no JWT auth, just X-Internal-Token).
func internalWsCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		if wsID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id query parameter required"})
			return
		}
		ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
