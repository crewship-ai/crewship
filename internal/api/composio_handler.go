// Package api — Composio managed-integration handler.
//
// Crewship is retiring self-hosted MCP connector management in favour of the
// Composio platform. This handler exposes the project's Composio state to the
// dashboard/CLI so an operator can see the connector catalog (auth configs)
// and the per-user inventory of connected accounts — the read-only foundation
// the agent-binding + MCP-URL generation (later slices) build on.
//
// Isolation model: a Composio `user_id` owns a set of connected accounts.
// Crewship binds each agent to one user_id, so an agent only ever sees that
// user's accounts. The full inventory below is an OPERATOR view (gated on
// "read") and is never handed to an agent.
package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/composio"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// ComposioHandler serves the /api/v1/integrations/composio/* routes. The
// effective API key is resolved per request: the workspace's stored key
// (composio_settings, encrypted) takes precedence, with the server
// COMPOSIO_API_KEY env as a fallback. This lets operators configure Composio
// from the UI without touching the server env.
type ComposioHandler struct {
	db         *sql.DB
	logger     *slog.Logger
	envKey     string // COMPOSIO_API_KEY fallback (empty when unset/disabled)
	envBaseURL string
}

// NewComposioHandler wires a handler. The env config is kept as a fallback
// only; the workspace-stored key (when present) wins at request time.
func NewComposioHandler(db *sql.DB, logger *slog.Logger, cfg *config.ComposioConfig) *ComposioHandler {
	h := &ComposioHandler{db: db, logger: logger}
	if cfg != nil && cfg.Enabled && cfg.APIKey != "" {
		h.envKey = cfg.APIKey
		h.envBaseURL = cfg.BaseURL
	}
	return h
}

// resolveClient builds a Composio client for the workspace and reports the
// config source ("workspace" | "env" | "" when unconfigured). Workspace row
// first, env fallback second. Any DB/decrypt error degrades to the env path
// (logged) rather than failing the request.
func (h *ComposioHandler) resolveClient(r *http.Request) (*composio.Client, string) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID != "" {
		var enc sql.NullString
		err := h.db.QueryRowContext(r.Context(),
			`SELECT encrypted_api_key FROM composio_settings WHERE workspace_id = ?`, wsID).Scan(&enc)
		switch {
		case err == nil && enc.Valid && enc.String != "":
			key, derr := encryption.Decrypt(enc.String)
			if derr != nil {
				h.logger.Error("composio: decrypt workspace key", "error", derr)
				break
			}
			if key != "" {
				// Base URL is never client-supplied (SSRF): only the server env
				// COMPOSIO_BASE_URL (h.envBaseURL) may pin a non-default host.
				return composio.NewClient(key, h.envBaseURL), "workspace"
			}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			// Table missing (pre-migration) or other read error: log and fall
			// through to the env key rather than 500.
			h.logger.Warn("composio: read workspace settings", "error", err)
		}
	}
	if h.envKey != "" {
		return composio.NewClient(h.envKey, h.envBaseURL), "env"
	}
	return nil, ""
}

// ── Response types ───────────────────────────────────────────────────────────

type composioInventoryResponse struct {
	Enabled     bool                    `json:"enabled"`
	AuthConfigs []composio.AuthConfig   `json:"auth_configs"`
	Users       []composioUserInventory `json:"users"`
}

// composioUserInventory groups one Composio user's connected accounts — the
// isolation unit Crewship binds agents against.
type composioUserInventory struct {
	UserID            string                      `json:"user_id"`
	ConnectedAccounts []composio.ConnectedAccount `json:"connected_accounts"`
}

// ── ListInventory — GET /api/v1/integrations/composio/inventory ──────────────

// ListInventory returns the connector catalog (auth configs) plus all
// connected accounts grouped by Composio user_id. Read-gated: feature-flag
// signal / connected-account metadata isn't enumerated for non-members.
func (h *ComposioHandler) ListInventory(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}

	client, _ := h.resolveClient(r)
	// Provider not configured: report disabled rather than failing, so the
	// dashboard renders the "connect Composio" empty state.
	if client == nil {
		writeJSON(w, http.StatusOK, composioInventoryResponse{
			Enabled:     false,
			AuthConfigs: []composio.AuthConfig{},
			Users:       []composioUserInventory{},
		})
		return
	}

	ctx := r.Context()
	authConfigs, err := client.ListAuthConfigs(ctx)
	if err != nil {
		h.logger.Error("composio: list auth configs", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	accounts, err := client.ListConnectedAccounts(ctx)
	if err != nil {
		h.logger.Error("composio: list connected accounts", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}

	resp := composioInventoryResponse{
		Enabled:     true,
		AuthConfigs: authConfigs,
		Users:       groupByUser(accounts),
	}
	if resp.AuthConfigs == nil {
		resp.AuthConfigs = []composio.AuthConfig{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── ListToolkits — GET /api/v1/integrations/composio/toolkits ────────────────

type composioToolkitsResponse struct {
	Enabled  bool                   `json:"enabled"`
	Total    int                    `json:"total"`
	Toolkits []composio.ToolkitInfo `json:"toolkits"`
}

// toolkitsPageLimit caps the catalog page we proxy from Composio (1000+ apps
// total). The UI drives narrowing via the search box rather than scrolling the
// whole catalog.
const toolkitsPageLimit = 40

// ListToolkits proxies the Composio app catalog (1000+ connectable apps) with
// optional search/category filters. Read-gated.
func (h *ComposioHandler) ListToolkits(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeJSON(w, http.StatusOK, composioToolkitsResponse{Enabled: false, Toolkits: []composio.ToolkitInfo{}})
		return
	}

	search := r.URL.Query().Get("search")
	category := r.URL.Query().Get("category")
	limit := toolkitsPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	page, err := client.ListToolkits(r.Context(), search, category, limit)
	if err != nil {
		h.logger.Error("composio: list toolkits", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	if page.Items == nil {
		page.Items = []composio.ToolkitInfo{}
	}
	writeJSON(w, http.StatusOK, composioToolkitsResponse{
		Enabled:  true,
		Total:    page.TotalItems,
		Toolkits: page.Items,
	})
}

// ── ListTools — GET /api/v1/integrations/composio/tools ──────────────────────

type composioToolsResponse struct {
	Enabled bool            `json:"enabled"`
	Total   int             `json:"total"`
	Tools   []composio.Tool `json:"tools"`
}

// toolsPageLimit caps the tools page we proxy from Composio. A single toolkit
// can expose hundreds of tools (GitHub: 846); the UI narrows via search.
const toolsPageLimit = 40

// ListTools proxies the tools a Composio toolkit exposes. `toolkit` is required
// (a 400 otherwise); `search` and `limit` (max 100, default 40) are optional.
// Read-gated; returns enabled:false when the provider is unconfigured.
func (h *ComposioHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeJSON(w, http.StatusOK, composioToolsResponse{Enabled: false, Tools: []composio.Tool{}})
		return
	}

	toolkit := strings.TrimSpace(r.URL.Query().Get("toolkit"))
	if toolkit == "" {
		writeProblem(w, r, http.StatusBadRequest, "toolkit is required")
		return
	}
	search := r.URL.Query().Get("search")
	limit := toolsPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	page, err := client.ListTools(r.Context(), toolkit, search, limit)
	if err != nil {
		h.logger.Error("composio: list tools", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	if page.Items == nil {
		page.Items = []composio.Tool{}
	}
	writeJSON(w, http.StatusOK, composioToolsResponse{
		Enabled: true,
		Total:   page.TotalItems,
		Tools:   page.Items,
	})
}

// ── ListTriggerTypes — GET /api/v1/integrations/composio/triggers ────────────

type composioTriggerTypesResponse struct {
	Enabled  bool                   `json:"enabled"`
	Total    int                    `json:"total"`
	Triggers []composio.TriggerType `json:"triggers"`
}

// triggerTypesPageLimit caps the trigger-types page we proxy from Composio. The
// UI narrows via the toolkit filter / search box.
const triggerTypesPageLimit = 40

// ListTriggerTypes proxies the available trigger types (event subscriptions
// like GMAIL_NEW_MESSAGE). `toolkit`, `search`, and `limit` (max 100, default
// 40) are optional server-side filters. Read-gated; returns enabled:false when
// the provider is unconfigured.
func (h *ComposioHandler) ListTriggerTypes(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeJSON(w, http.StatusOK, composioTriggerTypesResponse{Enabled: false, Triggers: []composio.TriggerType{}})
		return
	}

	toolkit := strings.TrimSpace(r.URL.Query().Get("toolkit"))
	search := r.URL.Query().Get("search")
	limit := triggerTypesPageLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	page, err := client.ListTriggerTypes(r.Context(), toolkit, search, limit)
	if err != nil {
		h.logger.Error("composio: list trigger types", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	if page.Items == nil {
		page.Items = []composio.TriggerType{}
	}
	writeJSON(w, http.StatusOK, composioTriggerTypesResponse{
		Enabled:  true,
		Total:    page.TotalItems,
		Triggers: page.Items,
	})
}

// ── ListActiveTriggers — GET /api/v1/integrations/composio/triggers/active ───

type composioActiveTriggersResponse struct {
	Enabled  bool                       `json:"enabled"`
	Triggers []composio.TriggerInstance `json:"triggers"`
}

// ListActiveTriggers proxies the live trigger instances in the project (all
// users). Read-gated; returns enabled:false when the provider is unconfigured.
func (h *ComposioHandler) ListActiveTriggers(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeJSON(w, http.StatusOK, composioActiveTriggersResponse{Enabled: false, Triggers: []composio.TriggerInstance{}})
		return
	}

	triggers, err := client.ListActiveTriggers(r.Context())
	if err != nil {
		h.logger.Error("composio: list active triggers", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	if triggers == nil {
		triggers = []composio.TriggerInstance{}
	}
	writeJSON(w, http.StatusOK, composioActiveTriggersResponse{Enabled: true, Triggers: triggers})
}

// ── CreateTrigger — POST /api/v1/integrations/composio/triggers ──────────────

type composioCreateTriggerRequest struct {
	Slug   string         `json:"slug"`
	UserID string         `json:"user_id"`
	Config map[string]any `json:"config"`
}

type composioCreateTriggerResponse struct {
	Enabled bool                     `json:"enabled"`
	Trigger composio.TriggerInstance `json:"trigger"`
}

// CreateTrigger creates (or re-enables) a trigger instance for a user. The body
// carries {slug, user_id, config?}: slug is the trigger-type slug
// (GMAIL_NEW_MESSAGE, …), user_id the Composio user that owns the connected
// account, config the trigger-type-specific configuration. OWNER/ADMIN only.
func (h *ComposioHandler) CreateTrigger(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}
	var req composioCreateTriggerRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	req.UserID = strings.TrimSpace(req.UserID)
	if req.Slug == "" || req.UserID == "" {
		writeProblem(w, r, http.StatusBadRequest, "slug and user_id are required")
		return
	}

	inst, err := client.CreateTriggerInstance(r.Context(), req.Slug, req.UserID, req.Config)
	if err != nil {
		h.logger.Error("composio: create trigger instance", "slug", req.Slug, "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	writeJSON(w, http.StatusOK, composioCreateTriggerResponse{Enabled: true, Trigger: inst})
}

// ── Connect — POST /api/v1/integrations/composio/connect ─────────────────────

type composioConnectRequest struct {
	Toolkit string `json:"toolkit"`
	UserID  string `json:"user_id"`
}

type composioConnectResponse struct {
	RedirectURL        string `json:"redirect_url"`
	ConnectedAccountID string `json:"connected_account_id"`
	UserID             string `json:"user_id"`
}

// Connect starts a hosted-auth (Connect Link) session so a user can authorise
// an app via OAuth. It ensures an auth config exists for the toolkit (creating
// a Composio-managed one if needed), then returns the redirect URL the caller
// opens in the browser. OWNER/ADMIN only.
func (h *ComposioHandler) Connect(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}
	var req composioConnectRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.Toolkit = strings.TrimSpace(req.Toolkit)
	req.UserID = strings.TrimSpace(req.UserID)
	if req.Toolkit == "" || req.UserID == "" {
		writeProblem(w, r, http.StatusBadRequest, "toolkit and user_id are required")
		return
	}

	ctx := r.Context()
	authID, err := client.FindAuthConfig(ctx, req.Toolkit)
	if err != nil {
		h.logger.Error("composio: find auth config", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	if authID == "" {
		authID, err = client.CreateManagedAuthConfig(ctx, req.Toolkit, req.Toolkit+"-crewship")
		if err != nil {
			h.logger.Error("composio: create auth config", "toolkit", req.Toolkit, "error", err)
			writeProblem(w, r, http.StatusBadGateway, "Composio API error (auth config)")
			return
		}
	}

	link, err := client.CreateConnectLink(ctx, authID, req.UserID, "")
	if err != nil {
		h.logger.Error("composio: create connect link", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error (connect link)")
		return
	}
	writeJSON(w, http.StatusOK, composioConnectResponse{
		RedirectURL:        link.RedirectURL,
		ConnectedAccountID: link.ConnectedAccountID,
		UserID:             req.UserID,
	})
}

// ── Agent binding — /api/v1/integrations/composio/agents/{agentId}/bind ──────
//
// Binding assigns a Composio user (its connected accounts/tools) to a specific
// agent. The agent then gets a per-user-scoped MCP server at runtime WITHOUT
// any change to resolveAgentMCPServers: we persist the three rows the existing
// resolver already reads —
//
//  1. credentials              — holds the Composio API key (type API_KEY,
//     so the binding's cred_type 'api_key' + cred_header 'x-api-key' makes the
//     sidecar inject `x-api-key: <key>` on the streamable-http MCP request).
//  2. workspace_mcp_servers    — points at the Composio MCP URL scoped to the
//     user (`?user_id=<id>`), transport streamable-http.
//  3. agent_mcp_bindings       — joins the agent to that workspace server +
//     the credential. The polymorphic-FK trigger requires (2) to exist first.
//
// All three are upserts on their UNIQUE keys so re-binding the same user is
// idempotent.

// composioBindRequest is the POST body for binding a Composio user to an agent.
type composioBindRequest struct {
	UserID   string   `json:"user_id"`
	Toolkits []string `json:"toolkits"`
}

type composioBindResponse struct {
	AgentID     string `json:"agent_id"`
	UserID      string `json:"user_id"`
	MCPServerID string `json:"mcp_server_id"`
	Endpoint    string `json:"endpoint"`
}

// composioManagedKeyName is the stable credentials.name the binding flow upserts
// the Composio API key under (one per workspace; UNIQUE(workspace_id,name)).
const composioManagedKeyName = "composio-managed-key"

// resolveKey mirrors resolveClient but returns the *decrypted* Composio API key
// and effective base URL for the workspace — the binding flow needs the raw key
// to persist it as a credential the sidecar can forward. ok is false when the
// provider is unconfigured. Workspace-stored key wins; env key is the fallback.
func (h *ComposioHandler) resolveKey(r *http.Request) (key, baseURL string, ok bool) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID != "" {
		var enc sql.NullString
		err := h.db.QueryRowContext(r.Context(),
			`SELECT encrypted_api_key FROM composio_settings WHERE workspace_id = ?`, wsID).Scan(&enc)
		switch {
		case err == nil && enc.Valid && enc.String != "":
			k, derr := encryption.Decrypt(enc.String)
			if derr != nil {
				h.logger.Error("composio: decrypt workspace key", "error", derr)
				break
			}
			if k != "" {
				// Base URL is env-only (see resolveClient): never the stored row.
				return k, h.envBaseURL, true
			}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			h.logger.Warn("composio: read workspace settings", "error", err)
		}
	}
	if h.envKey != "" {
		return h.envKey, h.envBaseURL, true
	}
	return "", "", false
}

// composioUserIDFromEndpoint extracts the `user_id` query param from a persisted
// Composio MCP endpoint, the inverse of the suffix BindAgent appends. Empty when
// the endpoint can't be parsed or carries no user_id.
func composioUserIDFromEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Query().Get("user_id")
}

// BindAgent assigns a Composio user to an agent: it ensures a Composio MCP
// server exists for the workspace, then persists the credential +
// workspace_mcp_servers + agent_mcp_bindings rows the runtime resolver reads.
// OWNER/ADMIN only.
func (h *ComposioHandler) BindAgent(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required")
		return
	}
	agentID := r.PathValue("agentId")
	if agentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent id required")
		return
	}

	client, _ := h.resolveClient(r)
	key, baseURL, ok := h.resolveKey(r)
	if client == nil || !ok {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}

	var req composioBindRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeProblem(w, r, http.StatusBadRequest, "user_id is required")
		return
	}

	ctx := r.Context()

	// Validate the agent belongs to this workspace before any external call /
	// write — a foreign agent id must 404, not provision a Composio server.
	var exists string
	if err := h.db.QueryRowContext(ctx,
		`SELECT id FROM agents WHERE id = ? AND workspace_id = ?`, agentID, wsID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "agent not found")
			return
		}
		h.logger.Error("composio: lookup agent", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Gather the auth-config ids the MCP server should expose. An EMPTY toolkits
	// list means "all of the workspace's auth configs" (every connected app) —
	// NOT zero; the filter below only narrows when at least one toolkit is named,
	// so the agent then sees just the requested apps.
	authConfigs, err := client.ListAuthConfigs(ctx)
	if err != nil {
		h.logger.Error("composio: list auth configs", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	wantToolkit := map[string]struct{}{}
	for _, t := range req.Toolkits {
		if t = strings.TrimSpace(strings.ToLower(t)); t != "" {
			wantToolkit[t] = struct{}{}
		}
	}
	var authConfigIDs []string
	for _, ac := range authConfigs {
		if len(wantToolkit) > 0 {
			if _, keep := wantToolkit[strings.ToLower(ac.Toolkit.Slug)]; !keep {
				continue
			}
		}
		authConfigIDs = append(authConfigIDs, ac.ID)
	}
	if len(authConfigIDs) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "no matching Composio auth configs (connect an app first)")
		return
	}

	// Composio MCP server name: 4–30 chars of [a-zA-Z0-9- ]. Derive a stable,
	// pattern-safe name from a short slice of the workspace id.
	wsShort := wsID
	if len(wsShort) > 12 {
		wsShort = wsShort[len(wsShort)-12:]
	}
	mcpName := "crewship-" + wsShort

	// Find-or-create: reuse the workspace's existing Composio MCP server when one
	// already carries this stable name — creating a fresh server on every bind
	// leaks duplicate Composio-side resources. A list error is non-fatal: fall
	// through to create (mcpURL stays empty) rather than block the binding.
	var mcpURL string
	if servers, lerr := client.ListMCPServers(ctx, mcpName); lerr != nil {
		h.logger.Warn("composio: list mcp servers", "error", lerr)
	} else {
		for _, s := range servers {
			if s.Name == mcpName && s.MCPURL != "" {
				mcpURL = s.MCPURL
				break
			}
		}
	}
	if mcpURL == "" {
		if _, mcpURL, err = client.CreateMCPServer(ctx, mcpName, authConfigIDs); err != nil {
			h.logger.Error("composio: create mcp server", "error", err)
			writeProblem(w, r, http.StatusBadGateway, "Composio API error (mcp server)")
			return
		}
	}
	if mcpURL == "" {
		h.logger.Error("composio: mcp server returned empty url")
		writeProblem(w, r, http.StatusBadGateway, "Composio API error (mcp url)")
		return
	}

	// Scope the URL to this Composio user. Preserve any existing query.
	sep := "?"
	if strings.Contains(mcpURL, "?") {
		sep = "&"
	}
	endpoint := mcpURL + sep + "user_id=" + url.QueryEscape(req.UserID)

	enc, err := encryption.Encrypt(key)
	if err != nil {
		h.logger.Error("composio: encrypt managed key", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var createdBy any
	if u := UserFromContext(ctx); u != nil {
		createdBy = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Persist all three rows in one transaction: a half-written binding (server
	// without credential, or binding without server) is worse than none.
	credID := generateCUID()
	serverID := generateCUID()
	bindingID := generateCUID()
	serverName := "composio-" + req.UserID

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.logger.Error("composio: begin bind tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	// (1) Credential — upsert on UNIQUE(workspace_id, name); capture the id of
	// whichever row now holds the key (existing or freshly inserted).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'API_KEY', 'CUSTOM', 'WORKSPACE', ?, ?, ?)
		ON CONFLICT(workspace_id, name) DO UPDATE SET
			encrypted_value = excluded.encrypted_value,
			type            = excluded.type,
			provider        = excluded.provider,
			scope           = excluded.scope,
			status          = 'ACTIVE',
			deleted_at      = NULL,
			updated_at      = excluded.updated_at`,
		credID, wsID, composioManagedKeyName, enc, createdBy, now, now); err != nil {
		h.logger.Error("composio: upsert managed credential", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM credentials WHERE workspace_id = ? AND name = ?`, wsID, composioManagedKeyName).Scan(&credID); err != nil {
		h.logger.Error("composio: read managed credential id", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// (2) workspace_mcp_servers — upsert on UNIQUE(workspace_id, name); refresh
	// the endpoint (the URL changes each time we create a fresh MCP server) and
	// clear any soft-delete so the FK trigger on (3) sees a live row.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport, endpoint, icon, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'streamable-http', ?, 'composio', 1, ?, ?)
		ON CONFLICT(workspace_id, name) DO UPDATE SET
			display_name = excluded.display_name,
			transport    = excluded.transport,
			endpoint     = excluded.endpoint,
			icon         = excluded.icon,
			enabled      = 1,
			deleted_at   = NULL,
			updated_at   = excluded.updated_at`,
		serverID, wsID, serverName, "Composio: "+req.UserID, endpoint, now, now); err != nil {
		h.logger.Error("composio: upsert mcp server row", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ?`, wsID, serverName).Scan(&serverID); err != nil {
		h.logger.Error("composio: read mcp server id", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// (3) agent_mcp_bindings — upsert on UNIQUE(agent_id, mcp_server_id,
	// mcp_server_scope). cred_type 'api_key' + cred_header 'x-api-key' is what
	// makes the sidecar inject the Composio key as the x-api-key header.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, cred_type, cred_header, enabled, created_at)
		VALUES (?, ?, ?, 'workspace', ?, 'api_key', 'x-api-key', 1, ?)
		ON CONFLICT(agent_id, mcp_server_id, mcp_server_scope) DO UPDATE SET
			credential_id = excluded.credential_id,
			cred_type     = excluded.cred_type,
			cred_header   = excluded.cred_header,
			enabled       = 1`,
		bindingID, agentID, serverID, credID, now); err != nil {
		h.logger.Error("composio: upsert agent binding", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("composio: commit bind", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	_ = baseURL // baseURL is captured for parity with resolveClient; the URL the
	// agent actually hits is the Composio-returned mcpURL, not the API base.

	writeJSON(w, http.StatusOK, composioBindResponse{
		AgentID:     agentID,
		UserID:      req.UserID,
		MCPServerID: serverID,
		Endpoint:    endpoint,
	})
}

// UnbindAgent removes an agent's Composio binding for a user. It deletes the
// agent_mcp_bindings row, and the workspace_mcp_servers row too when no other
// agent still binds it. OWNER/ADMIN only.
//
// DELETE /api/v1/integrations/composio/agents/{agentId}/bind?user_id=...
func (h *ComposioHandler) UnbindAgent(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required")
		return
	}
	agentID := r.PathValue("agentId")
	userID := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if agentID == "" || userID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent id and user_id are required")
		return
	}
	serverName := "composio-" + userID

	ctx := r.Context()
	// Resolve the workspace server row (scoped to this workspace) so we never
	// touch another workspace's binding even if ids were guessed.
	var serverID string
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ?`, wsID, serverName).Scan(&serverID)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}
	if err != nil {
		h.logger.Error("composio: lookup unbind server", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.logger.Error("composio: begin unbind tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_mcp_bindings WHERE agent_id = ? AND mcp_server_id = ? AND mcp_server_scope = 'workspace'`,
		agentID, serverID); err != nil {
		h.logger.Error("composio: delete agent binding", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Soft-delete the workspace server if no agent binds it anymore — keeps the
	// per-user server from lingering once its last agent is unbound.
	var remaining int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'workspace'`,
		serverID).Scan(&remaining); err != nil {
		h.logger.Error("composio: count remaining bindings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if remaining == 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE workspace_mcp_servers SET deleted_at = ?, updated_at = ? WHERE id = ? AND workspace_id = ?`,
			time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), serverID, wsID); err != nil {
			h.logger.Error("composio: soft-delete mcp server", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("composio: commit unbind", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// composioAgentBinding is one Composio binding on an agent (user_id + endpoint).
type composioAgentBinding struct {
	UserID   string `json:"user_id"`
	Endpoint string `json:"endpoint"`
}

type composioListBindingsResponse struct {
	AgentID  string                 `json:"agent_id"`
	Bindings []composioAgentBinding `json:"bindings"`
}

// ListAgentBindings returns the agent's Composio binding(s): the user_id (derived
// from the server endpoint) and the endpoint. Read-gated.
//
// GET /api/v1/integrations/composio/agents/{agentId}/bind
func (h *ComposioHandler) ListAgentBindings(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")
	if wsID == "" || agentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context and agent id required")
		return
	}

	// Composio-managed servers carry icon 'composio' and name 'composio-<user>'.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ws.endpoint, ws.name
		FROM agent_mcp_bindings b
		JOIN workspace_mcp_servers ws ON ws.id = b.mcp_server_id
		WHERE b.agent_id = ? AND b.mcp_server_scope = 'workspace'
		  AND ws.workspace_id = ? AND ws.deleted_at IS NULL AND ws.icon = 'composio'
		ORDER BY ws.name`, agentID, wsID)
	if err != nil {
		h.logger.Error("composio: list agent bindings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	bindings := []composioAgentBinding{}
	for rows.Next() {
		var endpoint sql.NullString
		var name string
		if err := rows.Scan(&endpoint, &name); err != nil {
			h.logger.Error("composio: scan agent binding", "error", err)
			continue
		}
		userID := composioUserIDFromEndpoint(endpoint.String)
		if userID == "" {
			userID = strings.TrimPrefix(name, "composio-")
		}
		bindings = append(bindings, composioAgentBinding{UserID: userID, Endpoint: endpoint.String})
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("composio: iterate agent bindings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, composioListBindingsResponse{AgentID: agentID, Bindings: bindings})
}

// ── Connected-account management — /accounts/{accountId}/{revoke,refresh} ────
//
// Lifecycle operations on an existing Composio connected account. Unlike the
// inventory (read) view, these mutate provider-side state, so they're all
// manage-gated. The account id is the Composio nanoid surfaced by the inventory
// endpoint; each handler proxies one Composio call and returns 204 on success.

// RevokeAccount de-authorizes a connected account at the provider (its
// credentials are invalidated upstream; the account must be re-connected to be
// usable again). OWNER/ADMIN only.
//
// POST /api/v1/integrations/composio/accounts/{accountId}/revoke
func (h *ComposioHandler) RevokeAccount(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if accountID == "" {
		writeProblem(w, r, http.StatusBadRequest, "account id required")
		return
	}
	if err := client.RevokeConnectedAccount(r.Context(), accountID); err != nil {
		h.logger.Error("composio: revoke connected account", "account", accountID, "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RefreshAccount refreshes a connected account's credentials (e.g. exchanging a
// refresh token for a new access token). OWNER/ADMIN only.
//
// POST /api/v1/integrations/composio/accounts/{accountId}/refresh
func (h *ComposioHandler) RefreshAccount(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if accountID == "" {
		writeProblem(w, r, http.StatusBadRequest, "account id required")
		return
	}
	if err := client.RefreshConnectedAccount(r.Context(), accountID); err != nil {
		h.logger.Error("composio: refresh connected account", "account", accountID, "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAccount permanently removes a connected account at the provider.
// OWNER/ADMIN only.
//
// DELETE /api/v1/integrations/composio/accounts/{accountId}
func (h *ComposioHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	client, _ := h.resolveClient(r)
	if client == nil {
		writeProblem(w, r, http.StatusBadRequest, "Composio is not configured (set an API key first)")
		return
	}
	accountID := strings.TrimSpace(r.PathValue("accountId"))
	if accountID == "" {
		writeProblem(w, r, http.StatusBadRequest, "account id required")
		return
	}
	if err := client.DeleteConnectedAccount(r.Context(), accountID); err != nil {
		h.logger.Error("composio: delete connected account", "account", accountID, "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Settings (API key) — /api/v1/integrations/composio/settings ──────────────

type composioSettingsResponse struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"` // "workspace" | "env" | "none"
	Label      string `json:"label,omitempty"`
}

// settingsState reports the effective config without ever returning the key.
func (h *ComposioHandler) settingsState(r *http.Request) composioSettingsResponse {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID != "" {
		var label sql.NullString
		err := h.db.QueryRowContext(r.Context(),
			`SELECT label FROM composio_settings WHERE workspace_id = ?`, wsID).Scan(&label)
		if err == nil {
			return composioSettingsResponse{Configured: true, Source: "workspace", Label: label.String}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("composio: read settings", "error", err)
		}
	}
	if h.envKey != "" {
		return composioSettingsResponse{Configured: true, Source: "env"}
	}
	return composioSettingsResponse{Configured: false, Source: "none"}
}

// GetSettings reports whether/how Composio is configured for the workspace.
func (h *ComposioHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	writeJSON(w, http.StatusOK, h.settingsState(r))
}

// UpsertSettings validates an API key against Composio, then stores it
// encrypted for the workspace. OWNER/ADMIN only.
func (h *ComposioHandler) UpsertSettings(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required")
		return
	}
	// base_url is intentionally NOT accepted from the API: letting a client pin
	// the Composio host would point the key-bearing client at an arbitrary/
	// internal target (SSRF). Only the server env COMPOSIO_BASE_URL may override
	// the default host; the stored base_url column is always written empty.
	var req struct {
		APIKey string `json:"api_key"`
		Label  string `json:"label"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey == "" {
		writeProblem(w, r, http.StatusBadRequest, "api_key is required")
		return
	}

	// Validate against Composio before persisting — a cheap authed call that
	// fails fast on a bad key/project (the UI surfaces this inline). The probe
	// uses the server-controlled base URL only (never a client-supplied one).
	probe := composio.NewClient(req.APIKey, h.envBaseURL)
	if _, err := probe.ListToolkits(r.Context(), "", "", 1); err != nil {
		h.logger.Warn("composio: api key validation failed", "error", err)
		writeProblem(w, r, http.StatusBadRequest, "Composio rejected this API key (check the key and project)")
		return
	}

	enc, err := encryption.Encrypt(req.APIKey)
	if err != nil {
		h.logger.Error("composio: encrypt api key", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var createdBy any
	if u := UserFromContext(r.Context()); u != nil {
		createdBy = u.ID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO composio_settings (workspace_id, encrypted_api_key, base_url, label, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			encrypted_api_key = excluded.encrypted_api_key,
			base_url          = excluded.base_url,
			label             = excluded.label,
			updated_at        = excluded.updated_at`,
		wsID, enc, "", req.Label, createdBy, now, now)
	if err != nil {
		h.logger.Error("composio: upsert settings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, composioSettingsResponse{
		Configured: true, Source: "workspace", Label: req.Label,
	})
}

// DeleteSettings removes the workspace's stored key, reverting to the env
// fallback (if any). OWNER/ADMIN only.
func (h *ComposioHandler) DeleteSettings(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required")
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM composio_settings WHERE workspace_id = ?`, wsID); err != nil {
		h.logger.Error("composio: delete settings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, h.settingsState(r))
}

// groupByUser folds connected accounts into per-user buckets, sorted by
// user_id for deterministic output (stable UI ordering + testable).
func groupByUser(accounts []composio.ConnectedAccount) []composioUserInventory {
	byUser := make(map[string][]composio.ConnectedAccount)
	for _, a := range accounts {
		byUser[a.UserID] = append(byUser[a.UserID], a)
	}
	users := make([]composioUserInventory, 0, len(byUser))
	for uid, accts := range byUser {
		users = append(users, composioUserInventory{UserID: uid, ConnectedAccounts: accts})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	return users
}
