package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

// restrictedRule is one entry of the restricted-member allowlist.
//
// A rule with a nil check is a blanket pass: the handler behind the pattern
// enforces the exact resource authority itself. A rule with a check passes
// only when check returns nil; access.ErrDenied and sql.ErrNoRows deny with
// 404, any other error is a 500.
type restrictedRule struct {
	check func(m *AuthMiddleware, store access.Store, r *http.Request, userID string) error
}

// restrictedPass is the rule for patterns whose handlers enforce their own authority.
var restrictedPass = restrictedRule{}

// restrictedAgentChatGrant requires a current agent/chat grant on {agentId}, checked
// in the (live) agent's own workspace.
var restrictedAgentChatGrant = restrictedRule{check: func(m *AuthMiddleware, store access.Store, r *http.Request, userID string) error {
	var workspace string
	err := m.db.QueryRowContext(r.Context(), `SELECT workspace_id FROM agents WHERE id=? AND deleted_at IS NULL`, r.PathValue("agentId")).Scan(&workspace)
	if err != nil {
		return err
	}
	return store.Check(r.Context(), userID, workspace, access.Right{Kind: "agent", ID: r.PathValue("agentId"), Operation: "chat"})
}}

// restrictedRoutes is the allowlist of integrated entrypoints for restricted
// members, keyed by the exact registered pattern (METHOD + path) that
// http.Request.Pattern carries. Anything not listed denies. Only add routes
// with an acceptance test, and update restrictedAllowedPatterns in
// restricted_route_table_test.go in the same change.
var restrictedRoutes = map[string]restrictedRule{
	// Native private-chat source projection enforces its own exact resource rights.
	"GET /api/v1/chats/{chatId}/project-input-options": restrictedPass,

	// Dedicated project source handler preserves exact grants and write role floor.
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files":                      restrictedPass,
	"GET /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{versionId}/download": restrictedPass,
	"POST /api/v1/workspaces/{workspaceId}/projects/{projectId}/files":                     restrictedPass,
	"DELETE /api/v1/workspaces/{workspaceId}/projects/{projectId}/files/{fileId}":          restrictedPass,

	// Classified context derives current exact chat authority.
	"GET /api/v1/chats/{chatId}/restricted-context":             restrictedPass,
	"POST /api/v1/chats/{chatId}/restricted-memory":             restrictedPass,
	"DELETE /api/v1/chats/{chatId}/restricted-memory/{entryId}": restrictedPass,

	// Source grants, brief and routine authority are checked in one private reservation.
	"POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight": restrictedPass,

	// Classified file handler derives the exact current audience.
	"GET /api/v1/chats/{chatId}/restricted-files":                   restrictedPass,
	"GET /api/v1/chats/{chatId}/restricted-files/{fileId}/download": restrictedPass,

	// Dedicated restricted directory projections expose granted metadata only.
	"GET /api/v1/agents":           restrictedPass,
	"GET /api/v1/agents/{agentId}": restrictedPass,
	"GET /api/v1/workspaces":       restrictedPass,

	// The member's own sessions, CLI tokens and password.
	"GET /api/v1/auth/sessions":                restrictedPass,
	"POST /api/v1/auth/sessions/{id}/revoke":   restrictedPass,
	"GET /api/v1/auth/cli-token/validate":      restrictedPass,
	"GET /api/v1/auth/cli-tokens":              restrictedPass,
	"DELETE /api/v1/auth/cli-tokens/{tokenId}": restrictedPass,
	"POST /api/v1/users/me/password":           restrictedPass,

	// Agent chat list and creation require the agent/chat grant.
	"GET /api/v1/agents/{agentId}/chats":  restrictedAgentChatGrant,
	"POST /api/v1/agents/{agentId}/chats": restrictedAgentChatGrant,

	// Restricted pages, routines and pipeline runs.
	"GET /api/v1/workspaces/{workspaceId}/restricted-pages":                restrictedPass,
	"GET /api/v1/workspaces/{workspaceId}/restricted-routines":             restrictedPass,
	"GET /api/v1/pages/{slug}/application/actions/{pendingId}":             restrictedPass,
	"POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run":           restrictedPass,
	"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}": restrictedPass,
	"GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs":         restrictedPass,
	"POST /api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}":        restrictedPass,
	"POST /api/v1/pages/{slug}/application/actions/{panelId}/{actionId}":   restrictedPass,

	// Exact audience and server admission are enforced by these handlers.
	"GET /api/v1/agents/{agentId}/run-profile":           restrictedPass,
	"POST /api/v1/agents/{agentId}/restricted-cli-chats": restrictedPass,
	"GET /api/v1/chats/{chatId}/execution-profile":       restrictedPass,
	"POST /api/v1/chats/{chatId}/restricted-run":         restrictedPass,
	"POST /api/v1/chats/{chatId}/restricted-cli-run":     restrictedPass,
	"GET /api/v1/chats/{chatId}/restricted-attempts":     restrictedPass,

	// ChatMessages applies the common current audience before proxying.
	"GET /api/v1/chats/{chatId}/messages": restrictedPass,
}

// restrictedRequest applies restrictedRoutes, evaluated AFTER authentication
// and BEFORE any legacy handler/IPC. Unclassified routes deny, including
// global routes with no workspace parameter. A mixed-membership user receives
// this conservative ceiling too; omitting/changing workspace is not a way
// back into the shared runtime.
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
	rule, ok := restrictedRoutes[r.Pattern]
	switch {
	case !ok:
		err = access.ErrDenied
	case rule.check != nil:
		err = rule.check(m, store, r, userID)
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
