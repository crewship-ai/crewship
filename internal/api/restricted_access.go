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
	case "GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files", "GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download", "POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files", "DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}":
		return true // dedicated project source handler preserves exact grants and write role floor

	case "GET /api/v1/chats/{chatId}/restricted-context", "POST /api/v1/chats/{chatId}/restricted-memory", "DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}":
		return true // classified context derives current exact chat authority
	case "POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight":
		return true // source grants, brief and routine authority are checked in one private reservation

	case "GET /api/v1/chats/{chatId}/restricted-files", "GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download":
		return true // classified file handler derives the exact current audience
	case "GET /api/v1/agents", "GET /api/v1/agents/{agentId}", "GET /api/v1/workspaces":
		// Dedicated restricted directory projections expose granted metadata only.
		return true
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
	case "POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run", "GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}", "GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs", "POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}", "POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}":
		return true
	case "GET /api/v1/agents/{agentId}/run-profile", "POST /api/v1/agents/{agentId}/restricted-cli-chats", "GET /api/v1/chats/{chatId}/execution-profile", "POST /api/v1/chats/{chatId}/restricted-run", "POST /api/v1/chats/{chatId}/restricted-cli-run", "GET /api/v1/chats/{chatId}/restricted-attempts":
		// Exact audience and server admission are enforced by these handlers.
		return true
	case "POST /api/v1/agents/{agentId}/chats":
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
