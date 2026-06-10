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
	// ctxInternalTokenWS carries the workspace a workspace-bound
	// X-Internal-Token is cryptographically bound to (PR-F24). Set by
	// requireInternal after HMAC validation; empty for master-token
	// callers (host-side trusted services). Distinct from
	// ctxWorkspaceID on purpose: this value is derived from the token
	// itself, never from caller-supplied query/body input.
	ctxInternalTokenWS contextKey = "internal_token_workspace"
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

// InternalTokenWorkspaceFromContext returns the workspace the request's
// X-Internal-Token is bound to (PR-F24), or "" when the caller
// authenticated with the unbound master token (host-side trusted
// services) or the request didn't pass requireInternal at all. Unlike
// WorkspaceIDFromContext, this value is derived from the token's HMAC
// — it cannot be forged by query parameters or request bodies.
func InternalTokenWorkspaceFromContext(ctx context.Context) string {
	s, _ := ctx.Value(ctxInternalTokenWS).(string)
	return s
}

// SecurityHeaders is middleware that sets standard security response headers.
// It does NOT set HSTS (the binary may run on plain HTTP). CSP defaults to
// the strict "default-src 'none'" policy because every API route returns
// JSON / SSE / WebSocket — no UI is ever served from the API router, so a
// Content-Type slip-up shouldn't be able to render HTML. The SPA paths get
// a separate, looser CSP from server.securityHeadersMiddleware.
//
// Exception: /exposed/ is mounted on the API router (mux.Handle("/exposed/",
// apiRouter) in internal/server/server.go) and reverse-proxies arbitrary
// upstream apps. We MUST NOT stamp our lockdown CSP on those responses or
// any HTML upstream will break. The skip mirrors the one in the server-
// level middleware — both layers must agree, which is why this CSP rule
// appears twice across two packages.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-XSS-Protection", "0")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if !strings.HasPrefix(r.URL.Path, "/exposed/") {
			h.Set("Content-Security-Policy",
				"default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
		}
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
// the user_sessions table (migration v63); pass *sessions.DBStore in
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
			// Pass the request ctx so the SELECT lookup respects the
			// caller's deadline; the audit metadata feeds the per-use
			// row written for ADMIN tokens so an incident responder
			// can answer "what did this admin token touch from where".
			audit := ValidateAuditContext{
				RemoteAddr: r.RemoteAddr,
				UserAgent:  r.Header.Get("User-Agent"),
				Path:       r.URL.Path,
			}
			// Patch M2: validate full result so we get the token's
			// scope set into the request context. Handler-side
			// canScope checks read it from there.
			res, err := ValidateCLITokenFull(r.Context(), m.db, token, audit)
			if err != nil {
				m.logger.Debug("CLI token auth failed", "error", err)
				writeAuthError(w, http.StatusUnauthorized, reasonSessionInvalid)
				return
			}
			user = &AuthUser{ID: res.UserID, Email: res.Email, Name: res.Name}
			if res.Scopes != nil {
				// Stash the scope set; later RequireAuth ctx mutation
				// merges it with the user context. Storing as the
				// concrete stringSet keeps canScope's lookup O(1).
				ctx := context.WithValue(r.Context(), ctxTokenScopes, res.Scopes)
				r = r.WithContext(ctx)
			}
		} else {
			claims, err := m.validator.ValidateAccess(token)
			if err != nil {
				m.logger.Debug("auth failed", "error", err)
				writeAuthError(w, http.StatusUnauthorized, reasonForJWTErr(err))
				return
			}

			// Tokens MUST carry a session id. Empty sid means either
			// (a) a token minted before migration v63 ever ran, or
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
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			sess, err := m.sessions.Get(r.Context(), claims.Sid)
			if err != nil {
				if errors.Is(err, sessions.ErrNotFound) {
					writeAuthError(w, http.StatusUnauthorized, reasonSessionRevoked)
					return
				}
				m.logger.Error("session lookup failed", "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
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
			replyError(w, http.StatusBadRequest, "workspace_id is required")
			return
		}

		var role string
		err := m.db.QueryRowContext(r.Context(),
			"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
			workspaceID, user.ID,
		).Scan(&role)
		if err != nil {
			replyError(w, http.StatusForbidden, "Not a member of this workspace")
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

// assertInternalTokenWorkspace rejects an internal-auth request whose
// body-declared workspace disagrees with the workspace bound to the
// caller's X-Internal-Token (PR-F24). requireInternal can only see
// query parameters; handlers that scope by a workspace_id carried in
// the JSON body (cost record, journal emit, pipeline save) call this
// after decoding so a captured sidecar token can't write rows into a
// foreign tenant either. No-op for master-token callers — those have
// no binding in context by design (host-side trusted services).
//
// Returns false (after writing the 403) when the values disagree —
// caller should immediately `return`.
func assertInternalTokenWorkspace(w http.ResponseWriter, r *http.Request, bodyWS string) bool {
	bound := InternalTokenWorkspaceFromContext(r.Context())
	if bound == "" || bound == bodyWS {
		return true
	}
	replyError(w, http.StatusForbidden,
		"workspace_id does not match the workspace bound to the internal token")
	return false
}

// internalWsCtx extracts workspace_id from query params and sets it in context.
// Used for internal routes called by sidecar (no JWT auth, just X-Internal-Token).
//
// PR-F24: when the request authenticated with a workspace-bound token,
// the query value must agree with the token's binding. requireInternal
// already rejects the mismatch upstream; the re-check here keeps the
// gate even if the middleware chain is ever reordered or a route gets
// wired with internalWsCtx but a different auth wrapper (the round-8
// lesson: never assume the other middleware ran).
func internalWsCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wsID := r.URL.Query().Get("workspace_id")
		if wsID == "" {
			replyError(w, http.StatusBadRequest, "workspace_id query parameter required")
			return
		}
		if bound := InternalTokenWorkspaceFromContext(r.Context()); bound != "" && bound != wsID {
			replyError(w, http.StatusForbidden,
				"workspace_id does not match the workspace bound to the internal token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
