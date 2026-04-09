package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/auth"
)

type contextKey string

const (
	ctxUser        contextKey = "user"
	ctxWorkspaceID contextKey = "workspace_id"
	ctxRole        contextKey = "role"
)

// AuthUser represents an authenticated user extracted from a JWT or CLI token.
type AuthUser struct {
	ID    string
	Email string
	Name  string
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

// AuthMiddleware provides HTTP middleware for JWT and CLI token authentication.
type AuthMiddleware struct {
	validator *auth.JWTValidator
	db        *sql.DB
	logger    *slog.Logger
}

// NewAuthMiddleware creates an AuthMiddleware with the given JWT validator and database connection.
func NewAuthMiddleware(validator *auth.JWTValidator, db *sql.DB, logger *slog.Logger) *AuthMiddleware {
	return &AuthMiddleware{validator: validator, db: db, logger: logger}
}

// RequireAuth returns middleware that validates the request's Bearer token or CLI token
// and stores the authenticated user in the request context.
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractToken(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
			return
		}

		var user *AuthUser

		// Check if this is a CLI token (crewship_cli_xxx)
		if IsCLIToken(token) {
			userID, email, name, err := ValidateCLIToken(m.db, token)
			if err != nil {
				m.logger.Debug("CLI token auth failed", "error", err)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
				return
			}
			user = &AuthUser{ID: userID, Email: email, Name: name}
		} else {
			claims, err := m.validator.Validate(token)
			if err != nil {
				m.logger.Debug("auth failed", "error", err)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
				return
			}
			user = &AuthUser{ID: claims.ID, Email: claims.Email, Name: claims.Name}
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
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
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
