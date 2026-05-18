package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CredentialHandler provides CRUD endpoints for managing encrypted credentials (API keys, tokens, OAuth).

type CredentialHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewCredentialHandler creates a CredentialHandler with the given database and logger.

func NewCredentialHandler(db *sql.DB, logger *slog.Logger) *CredentialHandler {
	return &CredentialHandler{db: db, logger: logger}
}

type credentialResponse struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  *string  `json:"description"`
	Type         string   `json:"type"`
	Provider     string   `json:"provider"`
	Status       string   `json:"status"`
	Scope        string   `json:"scope"`
	CrewID       *string  `json:"crew_id"`
	CrewIDs      []string `json:"crew_ids"`
	AccountLabel *string  `json:"account_label"`
	AccountEmail *string  `json:"account_email"`
	// Username is the cleartext identifier half of USERPASS credentials,
	// nil for every other type. Safe to expose because usernames are
	// not secrets — the password lives encrypted in encrypted_value
	// and is never returned by any read endpoint.
	Username       *string `json:"username"`
	TokenExpiresAt *string `json:"token_expires_at"`
	LastCheckedAt  *string `json:"last_checked_at"`
	LastError      *string `json:"last_error"`
	// LastUsedAt is the latest USE event recorded by RecordCredentialEvent.
	// Distinct from LastCheckedAt — that's a health-check timestamp.
	// Drives the Stale status (last_used_at < now-90d) in the 5-state
	// taxonomy from CONNECTIONS.md §3.4.
	LastUsedAt *string `json:"last_used_at"`
	// LastUsedIPs is the parsed JSON array (max 5, ringbuffer) so the
	// FE doesn't have to second-parse an embedded JSON string.
	LastUsedIPs []string `json:"last_used_ips"`
	// Tags is the parsed JSON array of free-form labels. Always
	// non-nil so the FE can iterate without a null check.
	Tags       []string `json:"tags"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	AgentCount int      `json:"_count_agent_credentials"`
	AgentNames []string `json:"agent_names"`
	MCPUsed    bool     `json:"mcp_used"`
}

// Batch loaders and junction-table helpers live in credentials_loaders.go
// to keep this file focused on HTTP handler methods.

// List returns all credentials in the workspace (without secret values).
// GET /api/v1/credentials

func (h *CredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	limit, offset := parseListPagination(r, 100, 500)

	// Crew-scoped visibility: roles below MANAGER (= MEMBER, VIEWER)
	// see workspace-scoped credentials plus credentials assigned to
	// crews they belong to. MANAGER+ see everything in the workspace
	// — they're the ones who own the credential lifecycle. The split
	// happens at the SQL level so we never serialise rows the caller
	// can't see.
	visFilter, visArgs := credentialVisibilityFilter(role, user)
	args := append([]any{workspaceID}, visArgs...)
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT c.id, c.name, c.description, c.type, c.provider, c.status,
			c.scope, c.crew_id, c.account_label, c.account_email, c.username,
			c.token_expires_at, c.last_checked_at, c.last_error,
			c.last_used_at, c.last_used_ips, c.tags,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agent_credentials WHERE credential_id = c.id) AS agent_count
		FROM credentials c
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL `+visFilter+`
		-- c.id ASC is the pagination tiebreaker: (type, created_at) alone can
		-- tie on bulk-imported credentials sharing a second, and ties that
		-- straddle a page boundary would drop or duplicate rows.
		ORDER BY c.type ASC, c.created_at DESC, c.id ASC
		LIMIT ? OFFSET ?
	`, args...)
	if err != nil {
		h.logger.Error("list credentials", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []credentialResponse
	for rows.Next() {
		var c credentialResponse
		var lastUsedIPsRaw, tagsRaw sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
			&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail, &c.Username,
			&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
			&c.LastUsedAt, &lastUsedIPsRaw, &tagsRaw,
			&c.CreatedAt, &c.UpdatedAt, &c.AgentCount); err != nil {
			h.logger.Error("scan credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		c.LastUsedIPs = parseLastUsedIPs(lastUsedIPsRaw)
		c.Tags = parseTags(tagsRaw)
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (credentials)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []credentialResponse{}
	}

	// Batch-load crew_ids from junction table
	credIDs := make([]string, len(result))
	for i, c := range result {
		credIDs[i] = c.ID
	}
	crewIDsMap := h.loadCrewIDsBatch(r.Context(), credIDs)
	agentNamesMap := h.loadAgentNamesBatch(r.Context(), credIDs)
	mcpUsedSet := h.loadMCPUsedBatch(r.Context(), credIDs)
	for i := range result {
		if ids, ok := crewIDsMap[result[i].ID]; ok {
			result[i].CrewIDs = ids
		} else {
			result[i].CrewIDs = []string{}
		}
		if names, ok := agentNamesMap[result[i].ID]; ok {
			result[i].AgentNames = names
		} else {
			result[i].AgentNames = []string{}
		}
		result[i].MCPUsed = mcpUsedSet[result[i].ID]
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *CredentialHandler) Get(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())

	visFilter, visArgs := credentialVisibilityFilter(role, user)
	args := append([]any{credID, workspaceID}, visArgs...)

	var c credentialResponse
	var lastUsedIPsRaw, tagsRaw sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.name, c.description, c.type, c.provider, c.status,
			c.scope, c.crew_id, c.account_label, c.account_email, c.username,
			c.token_expires_at, c.last_checked_at, c.last_error,
			c.last_used_at, c.last_used_ips, c.tags,
			c.created_at, c.updated_at,
			(SELECT COUNT(*) FROM agent_credentials WHERE credential_id = c.id) AS agent_count
		FROM credentials c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL `+visFilter+`
	`, args...).Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
		&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail, &c.Username,
		&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
		&c.LastUsedAt, &lastUsedIPsRaw, &tagsRaw,
		&c.CreatedAt, &c.UpdatedAt, &c.AgentCount)
	c.LastUsedIPs = parseLastUsedIPs(lastUsedIPsRaw)
	c.Tags = parseTags(tagsRaw)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Credential not found")
			return
		}
		h.logger.Error("get credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	c.CrewIDs = h.loadCrewIDs(r.Context(), c.ID)
	if names, ok := h.loadAgentNamesBatch(r.Context(), []string{c.ID})[c.ID]; ok {
		c.AgentNames = names
	} else {
		c.AgentNames = []string{}
	}
	c.MCPUsed = h.loadMCPUsedBatch(r.Context(), []string{c.ID})[c.ID]
	writeJSON(w, http.StatusOK, c)
}

// Update modifies credential metadata and optionally rotates the encrypted secret value.
// PATCH /api/v1/credentials/{credentialId}

func (h *CredentialHandler) Delete(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE credentials SET deleted_at = ? WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		now, credID, workspaceID)
	if err != nil {
		h.logger.Error("delete credential", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	// Clear credential references from agent bindings so integrations
	// show a "credential missing" warning in the UI.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agent_mcp_bindings SET credential_id = NULL WHERE credential_id = ?", credID); err != nil {
		h.logger.Warn("clear credential from MCP bindings", "credential_id", credID, "error", err)
	}

	// Stamp the timeline so the audit tab still answers "who deleted
	// this and when" after the row is soft-deleted. credential_audit
	// rows survive soft-delete (no FK cascade), so the historical
	// record is preserved.
	user := UserFromContext(r.Context())
	var deletedBy string
	if user != nil {
		deletedBy = user.ID
	}
	if recErr := RecordCredentialEvent(r.Context(), h.db, h.logger, credID, AuditEventRevoke, "", clientIP(r),
		map[string]any{"deleted_by": deletedBy, "soft_delete": true}); recErr != nil {
		h.logger.Warn("record REVOKE audit event", "error", recErr, "credential_id", credID)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Test validates a credential value against the provider's API without storing it.
// POST /api/v1/credentials/test

func (h *CredentialHandler) DefaultEnvVar(w http.ResponseWriter, r *http.Request) {
	prov := r.URL.Query().Get("provider")
	envVar := defaultEnvVarForCLIProvider(prov)
	writeJSON(w, http.StatusOK, map[string]string{"env_var": envVar})
}

func defaultEnvVarForCLIProvider(provider string) string {
	switch provider {
	case "GITHUB":
		return "GH_TOKEN"
	case "GITLAB":
		return "GITLAB_TOKEN"
	case "VERCEL":
		return "VERCEL_TOKEN"
	case "AWS":
		return "AWS_ACCESS_KEY_ID"
	case "KUBERNETES":
		return "KUBECONFIG"
	default:
		return ""
	}
}

// isAnthropicOAuthToken detects if a value is an Anthropic OAuth/setup token
// rather than a plain API key. OAuth tokens use "sk-ant-oat" prefix.

func isAnthropicOAuthToken(value string) bool {
	return strings.HasPrefix(value, "sk-ant-oat")
}
