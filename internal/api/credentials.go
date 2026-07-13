package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/credprovider"
	"github.com/crewship-ai/crewship/internal/provider"
)

// CredentialHandler provides CRUD endpoints for managing encrypted credentials (API keys, tokens, OAuth).

type CredentialHandler struct {
	db     *sql.DB
	logger *slog.Logger
	// container reaches running crew containers to reconcile file-based
	// /secrets on revoke (#814). nil when Docker isn't wired (tests,
	// --no-docker) — reconciliation then no-ops. Set via SetContainer.
	container provider.ContainerProvider
}

// decryptEndpointURLForRead returns the decrypted endpoint URL for an
// ENDPOINT_URL credential so read endpoints can echo it (a base URL is a
// destination, not a secret — same reasoning as the cleartext username).
// Returns nil for every other type, for a PENDING sentinel body, or on a
// decrypt error — the value must never leak for a secret type, and a read
// path must not 500 on one bad row.
func decryptEndpointURLForRead(credType, encValue string, logger *slog.Logger) *string {
	if credType != CredTypeEndpointURL || encValue == "" {
		return nil
	}
	dec, err := decryptCredential(encValue)
	if err != nil {
		if logger != nil {
			logger.Warn("decrypt ENDPOINT_URL for read", "error", err)
		}
		return nil
	}
	if dec == "" || isPendingSentinel(dec) {
		return nil
	}
	// #961: the stored value may be a bare URL or a {baseURL,apiKey,headers}
	// object. Echo ONLY the base URL — the auth token/headers must never
	// leave the server through a read path.
	baseURL, _, _, err := parseEndpointValue(dec)
	if err != nil || baseURL == "" {
		return nil
	}
	return &baseURL
}

// NewCredentialHandler creates a CredentialHandler with the given database and logger.

func NewCredentialHandler(db *sql.DB, logger *slog.Logger) *CredentialHandler {
	return &CredentialHandler{db: db, logger: logger}
}

// SetContainer wires the container provider used to remove revoked file-based
// credentials from running containers (#814).
func (h *CredentialHandler) SetContainer(cp provider.ContainerProvider) { h.container = cp }

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
	Username *string `json:"username"`
	// EndpointURL is the decrypted value of an ENDPOINT_URL credential
	// (#955) — an OpenAI-compatible base URL, not a secret, so it is
	// echoed back on read the way Username is. nil for every other type;
	// no read endpoint ever returns the value of a secret credential.
	EndpointURL    *string `json:"endpoint_url,omitempty"`
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
	// Attribution (v98). CreatedByActorType is one of 'user' /
	// 'agent' / 'system'; nil only on pre-v98 rows that the
	// migration hasn't backfilled to 'user' (no such case in
	// SQLite since the default kicks in, kept *string for forward-
	// compat with future schema changes).
	CreatedByActorType    *string `json:"created_by_actor_type"`
	CreatedByActorID      *string `json:"created_by_actor_id"`
	ProvisionedForService *string `json:"provisioned_for_service"`
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
			c.created_by_actor_type, c.created_by_actor_id, c.provisioned_for_service,
			c.encrypted_value,
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
		var encValue string
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
			&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail, &c.Username,
			&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
			&c.LastUsedAt, &lastUsedIPsRaw, &tagsRaw,
			&c.CreatedAt, &c.UpdatedAt,
			&c.CreatedByActorType, &c.CreatedByActorID, &c.ProvisionedForService,
			&encValue,
			&c.AgentCount); err != nil {
			h.logger.Error("scan credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		c.LastUsedIPs = parseLastUsedIPs(lastUsedIPsRaw)
		c.Tags = parseTags(tagsRaw)
		c.EndpointURL = decryptEndpointURLForRead(c.Type, encValue, h.logger)
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
	var encValue string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT c.id, c.name, c.description, c.type, c.provider, c.status,
			c.scope, c.crew_id, c.account_label, c.account_email, c.username,
			c.token_expires_at, c.last_checked_at, c.last_error,
			c.last_used_at, c.last_used_ips, c.tags,
			c.created_at, c.updated_at,
			c.created_by_actor_type, c.created_by_actor_id, c.provisioned_for_service,
			c.encrypted_value,
			(SELECT COUNT(*) FROM agent_credentials WHERE credential_id = c.id) AS agent_count
		FROM credentials c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL `+visFilter+`
	`, args...).Scan(&c.ID, &c.Name, &c.Description, &c.Type, &c.Provider,
		&c.Status, &c.Scope, &c.CrewID, &c.AccountLabel, &c.AccountEmail, &c.Username,
		&c.TokenExpiresAt, &c.LastCheckedAt, &c.LastError,
		&c.LastUsedAt, &lastUsedIPsRaw, &tagsRaw,
		&c.CreatedAt, &c.UpdatedAt,
		&c.CreatedByActorType, &c.CreatedByActorID, &c.ProvisionedForService,
		&encValue,
		&c.AgentCount)
	c.LastUsedIPs = parseLastUsedIPs(lastUsedIPsRaw)
	c.Tags = parseTags(tagsRaw)
	c.EndpointURL = decryptEndpointURLForRead(c.Type, encValue, h.logger)
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
	user := UserFromContext(r.Context())
	callerUserID := ""
	if user != nil {
		callerUserID = user.ID
	}

	// Patch M4: structured 403 — audit-friendly WARN with
	// subject/action/resource so an operator chasing "why did Alice
	// get a 403?" doesn't have to grep a wall of generic Forbidden
	// lines. Behaviour identical (canRole "manage" = OWNER/ADMIN).
	if !requireRoleOrForbid(w, h.logger, callerUserID, role,
		"credential.delete", "credential:"+credID, "manage") {
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

	// Remove the agent_credentials assignments (#1050). Delete is a SOFT delete
	// (deleted_at), so the `ON DELETE CASCADE` FK never fires — the assignment
	// join rows would otherwise linger, keeping the credential listed as
	// "assigned" and inflating the per-agent credential counts (agents_query.go)
	// long after it's gone. agent_credentials has no independent value once the
	// credential is deleted, so a hard delete is correct here.
	if _, err := h.db.ExecContext(r.Context(),
		"DELETE FROM agent_credentials WHERE credential_id = ?", credID); err != nil {
		h.logger.Warn("remove credential assignments on delete", "credential_id", credID, "error", err)
	}

	// Stamp the timeline so the audit tab still answers "who deleted
	// this and when" after the row is soft-deleted. credential_audit
	// rows survive soft-delete (no FK cascade), so the historical
	// record is preserved.
	deletedBy := callerUserID
	recordCredentialEventBestEffort(r.Context(), h.db, h.logger, credID, AuditEventRevoke, "", clientIP(r),
		map[string]any{"deleted_by": deletedBy, "soft_delete": true})

	// Reach into any running crew container and remove this credential's
	// materialized /secrets file(s) so a revoke actually takes effect for a
	// live agent, not just on the next boot (#814). Detached from the request
	// context + bounded, so a client disconnect can't abort the removal and a
	// wedged exec can't hang the response; best-effort (the DB revoke above
	// already succeeded).
	rctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	h.reconcileRevokedCredential(rctx, credID, workspaceID)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// Test validates a credential value against the provider's API without storing it.
// POST /api/v1/credentials/test

func (h *CredentialHandler) DefaultEnvVar(w http.ResponseWriter, r *http.Request) {
	prov := r.URL.Query().Get("provider")
	// #1083: the provider→env-var map is single-sourced in internal/credprovider
	// so this handler and the CLI's --provider help can't drift.
	envVar := credprovider.DefaultEnvVar(prov)
	writeJSON(w, http.StatusOK, map[string]string{"env_var": envVar})
}

// isAnthropicOAuthToken detects if a value is an Anthropic OAuth/setup token
// rather than a plain API key. OAuth tokens use "sk-ant-oat" prefix.

func isAnthropicOAuthToken(value string) bool {
	return strings.HasPrefix(value, "sk-ant-oat")
}
