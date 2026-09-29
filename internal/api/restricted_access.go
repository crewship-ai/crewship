package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

// restrictedRequest is an allowlist of integrated entrypoints, evaluated AFTER
// authentication and BEFORE any legacy handler/IPC. Unclassified routes deny,
// including global routes with no workspace parameter. A mixed-membership user
// receives this conservative ceiling too; omitting/changing workspace is not a
// way back into the shared runtime. Only add routes with an acceptance test.
func (m *AuthMiddleware) restrictedRequest(w http.ResponseWriter, r *http.Request, userID string) bool {
	store := access.Store{DB: m.db}
	restricted, err := store.HasRestrictedMembership(r.Context(), userID)
	if err != nil {
		replyInternalError(w, m.logger, "resolve resource authority", err)
		return false
	}
	if !restricted {
		return true
	}
	switch r.Pattern {
	case "GET /api/v1/auth/sessions", "POST /api/v1/auth/sessions/{id}/revoke",
		"GET /api/v1/auth/cli-token/validate", "GET /api/v1/auth/cli-tokens",
		"DELETE /api/v1/auth/cli-tokens/{tokenId}", "POST /api/v1/users/me/password":
		return true
	case "GET /api/v1/agents/{agentId}/chats":
		var workspace string
		err = m.db.QueryRowContext(r.Context(), `SELECT workspace_id FROM agents WHERE id=? AND deleted_at IS NULL`, r.PathValue("agentId")).Scan(&workspace)
		if err == nil {
			err = store.Check(r.Context(), userID, workspace, access.Right{Kind: "agent", ID: r.PathValue("agentId"), Operation: "chat"})
		}
	case "GET /api/v1/chats/{chatId}/messages":
		// ChatMessages applies the common current audience before proxying.
		return true
	default:
		err = access.ErrDenied
	}
	if err == nil {
		return true
	}
	if errors.Is(err, access.ErrDenied) || errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Resource not found")
	} else {
		replyInternalError(w, m.logger, "check resource authority", err)
	}
	return false
}
