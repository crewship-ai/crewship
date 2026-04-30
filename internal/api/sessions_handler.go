package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// SessionsHandler exposes the user-facing "Active sessions" surface:
// GET /api/v1/auth/sessions    — list active sessions for the caller
// POST /api/v1/auth/sessions/{id}/revoke — kill a specific session
//
// Revoking the caller's own session is allowed; the frontend handles
// the resulting 401 by hard-redirecting to /login. Revoking someone
// else's session is forbidden by the user_id ownership check.
type SessionsHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	sessions sessions.Store
}

// NewSessionsHandler wires the active-sessions endpoints. The same
// store the auth middleware uses must be passed in — otherwise
// "revoke" wouldn't actually flip the row the middleware checks.
func NewSessionsHandler(db *sql.DB, logger *slog.Logger, store sessions.Store) *SessionsHandler {
	return &SessionsHandler{db: db, logger: logger, sessions: store}
}

// sessionDTO is the wire shape for /api/v1/auth/sessions. We avoid
// returning revoked_at / revoked_reason / user_id because the list
// is filtered to active+self already; emitting those would just be
// dead fields the UI would have to skip.
type sessionDTO struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	UserAgent  string `json:"user_agent,omitempty"`
	IP         string `json:"ip,omitempty"`
	IsCurrent  bool   `json:"is_current"`
}

// List returns the caller's active sessions, newest-active first.
// is_current marks the row backing this request so the UI can render
// "this device" with a different label and warn before revoking it.
func (h *SessionsHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeAuthError(w, http.StatusUnauthorized, reasonNoCredentials)
		return
	}

	rows, err := h.sessions.ListActiveForUser(r.Context(), user.ID)
	if err != nil {
		h.logger.Error("list sessions", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	out := make([]sessionDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, sessionDTO{
			ID:         s.ID,
			CreatedAt:  s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastUsedAt: s.LastUsedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UserAgent:  s.UserAgent,
			IP:         s.IP,
			IsCurrent:  s.ID == user.SessionID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// Revoke flips revoked_at on a single session owned by the caller. The
// 404 path is preserved for "doesn't exist" and "isn't yours" alike so
// callers can't enumerate other users' session ids by guessing.
func (h *SessionsHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeAuthError(w, http.StatusUnauthorized, reasonNoCredentials)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id required"})
		return
	}

	sess, err := h.sessions.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
			return
		}
		h.logger.Error("get session for revoke", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if sess.UserID != user.ID {
		// Ownership check. Don't leak existence — same response as 404.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	if err := h.sessions.Revoke(r.Context(), id, sessions.ReasonAdminForce); err != nil {
		h.logger.Error("revoke session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":         true,
		"id":         id,
		"is_current": id == user.SessionID,
	})
}
