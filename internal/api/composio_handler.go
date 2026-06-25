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
	"fmt"
	"hash/fnv"
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

// composioAppScope is one granted app in a bind request: a toolkit plus the
// tool scope to expose for it.
//
//	mode "full"   → every tool the app has (allowed_tools empty on the MCP server).
//	mode "read"   → only the app's read-ish tools (resolved server-side via isReadTool).
//	mode "custom" → exactly the slugs in Tools (validated against the app's real tools).
type composioAppScope struct {
	Toolkit string   `json:"toolkit"`
	Mode    string   `json:"mode"`
	Tools   []string `json:"tools"`
}

// composioBindRequest is the POST body for binding a Composio user to an agent.
// The current shape is {user_id, apps:[{toolkit,mode,tools}]}; the legacy
// {user_id, toolkits:[...]} shape is still accepted (each toolkit ⇒ an app at
// mode "full") for backward compatibility.
type composioBindRequest struct {
	UserID   string             `json:"user_id"`
	Apps     []composioAppScope `json:"apps"`
	Toolkits []string           `json:"toolkits"`
}

// composioReadVerbs is the token set isReadTool matches a tool slug against to
// classify it as read-only. Conservative on purpose: an unrecognised verb is
// treated as NOT read so a read-mode binding never over-grants a write tool.
var composioReadVerbs = map[string]struct{}{
	"GET": {}, "LIST": {}, "FETCH": {}, "SEARCH": {}, "READ": {}, "FIND": {},
	"DOWNLOAD": {}, "EXPORT": {}, "RETRIEVE": {}, "COUNT": {}, "CHECK": {},
	"VIEW": {}, "GETPROFILE": {},
}

// isReadTool reports whether a Composio tool slug looks read-only. It strips the
// leading toolkit prefix (e.g. "GMAIL_") and checks whether any remaining
// underscore-token is one of composioReadVerbs (case-insensitive). Heuristic and
// deliberately conservative — when unsure it returns false so we never expose a
// mutating tool under a "read" scope.
func isReadTool(slug string) bool {
	s := strings.ToUpper(strings.TrimSpace(slug))
	if i := strings.Index(s, "_"); i >= 0 {
		s = s[i+1:]
	}
	for _, tok := range strings.Split(s, "_") {
		if _, ok := composioReadVerbs[tok]; ok {
			return true
		}
	}
	return false
}

// composioScopeTag derives a short, name-safe tag identifying a tool-scope set,
// so the per-(app,scope) Composio MCP server name stays stable and within
// Composio's 30-char [a-zA-Z0-9- ] limit. Empty allowedTools (full scope) → "full";
// otherwise an 8-hex FNV hash of the sorted, lowercased tool slugs (the set, not
// the order). Identical scopes reuse the same Composio server.
func composioScopeTag(allowedTools []string) string {
	if len(allowedTools) == 0 {
		return "full"
	}
	tools := make([]string, len(allowedTools))
	for i, t := range allowedTools {
		tools[i] = strings.ToLower(t)
	}
	sort.Strings(tools)
	hsh := fnv.New32a()
	_, _ = hsh.Write([]byte(strings.Join(tools, ",")))
	return fmt.Sprintf("%08x", hsh.Sum32())
}

// composioServerName builds the Composio MCP server name for one (workspace,
// toolkit, scope). Composio caps names at 30 chars of [a-zA-Z0-9- ], so we use
// the last 8 chars of the workspace id and, when the toolkit slug would push the
// name over the limit, replace the slug with an 8-hex FNV hash of it (the server
// stays unique because the hash is deterministic per slug). The "full"/scope-hash
// suffix keeps different scopes of the same app on different servers.
func composioServerName(wsID, toolkitSlug, scopeTag string) string {
	wsShort := wsID
	if len(wsShort) > 8 {
		wsShort = wsShort[len(wsShort)-8:]
	}
	slug := strings.ToLower(toolkitSlug)
	name := "crewship-" + wsShort + "-" + slug + "-" + scopeTag
	if len(name) > 30 {
		// Composio caps names at 30 chars. Collapse slug+scope into a single
		// 8-hex hash so the name is always "crewship-<ws8>-<8hex>" = 26 chars,
		// deterministic per (slug, scope) within the workspace (read/custom apps
		// and long slugs like googlecalendar land here).
		hsh := fnv.New32a()
		_, _ = hsh.Write([]byte(slug + ":" + scopeTag))
		name = "crewship-" + wsShort + "-" + fmt.Sprintf("%08x", hsh.Sum32())
	}
	return name
}

// composioModeLabel renders the human-readable scope label embedded in a
// workspace_mcp_servers display_name. ListAgentBindings parses the mode back out
// of this label (storage has no dedicated mode column).
func composioModeLabel(mode string, toolCount int) string {
	switch mode {
	case "read":
		return "Read-only"
	case "custom":
		return fmt.Sprintf("Custom %d", toolCount)
	default:
		return "Full"
	}
}

// composioModeFromDisplay is the inverse of composioModeLabel: it recovers the
// scope mode from a stored display_name ("…· Full" / "…· Read-only" / "…· Custom N").
func composioModeFromDisplay(display string) string {
	idx := strings.LastIndex(display, "·")
	if idx < 0 {
		return ""
	}
	label := strings.TrimSpace(display[idx+len("·"):])
	switch {
	case strings.HasPrefix(label, "Read"):
		return "read"
	case strings.HasPrefix(label, "Custom"):
		return "custom"
	case strings.HasPrefix(label, "Full"):
		return "full"
	}
	return ""
}

// composioBindAppResult is one provisioned app in the bind response.
type composioBindAppResult struct {
	Toolkit  string `json:"toolkit"`
	Mode     string `json:"mode"`
	Endpoint string `json:"endpoint"`
}

type composioBindResponse struct {
	AgentID string                  `json:"agent_id"`
	UserID  string                  `json:"user_id"`
	Apps    []composioBindAppResult `json:"apps"`
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

// normalizeComposioApps resolves the granted app set from a bind request into a
// deduped, lower-cased list with a default mode of "full". Priority: explicit
// apps[] → legacy toolkits[] (each at full) → when both empty, every connected
// app (from acBySlug) at full. The first occurrence of a toolkit wins.
func normalizeComposioApps(req composioBindRequest, acBySlug map[string]string) []composioAppScope {
	var raw []composioAppScope
	switch {
	case len(req.Apps) > 0:
		raw = req.Apps
	case len(req.Toolkits) > 0:
		for _, t := range req.Toolkits {
			raw = append(raw, composioAppScope{Toolkit: t, Mode: "full"})
		}
	default:
		slugs := make([]string, 0, len(acBySlug))
		for s := range acBySlug {
			slugs = append(slugs, s)
		}
		sort.Strings(slugs)
		for _, s := range slugs {
			raw = append(raw, composioAppScope{Toolkit: s, Mode: "full"})
		}
	}
	seen := map[string]struct{}{}
	out := make([]composioAppScope, 0, len(raw))
	for _, a := range raw {
		slug := strings.ToLower(strings.TrimSpace(a.Toolkit))
		if slug == "" {
			continue
		}
		if _, dup := seen[slug]; dup {
			continue
		}
		seen[slug] = struct{}{}
		mode := strings.ToLower(strings.TrimSpace(a.Mode))
		if mode == "" {
			mode = "full"
		}
		out = append(out, composioAppScope{Toolkit: slug, Mode: mode, Tools: a.Tools})
	}
	return out
}

// composioResolvedApp is one granted app after its Composio MCP server has been
// provisioned, carrying everything the DB transaction needs.
type composioResolvedApp struct {
	toolkit   string
	mode      string
	serverRow string // workspace_mcp_servers.name ("composio-<agentID>-<toolkit>")
	display   string
	endpoint  string
}

// BindAgent grants an agent PER-APP, tool-scoped access to a Composio user's
// connected apps. For each granted app it provisions (find-or-create) a Composio
// MCP server scoped to THAT app's auth config with an allowed_tools set matching
// the requested mode (full → all tools, read → the app's read-ish tools, custom →
// the picked tools), then persists the workspace_mcp_servers + agent_mcp_bindings
// rows the runtime resolver reads. Apps the agent previously had but that aren't
// in this request are removed (de-selection). OWNER/ADMIN only.
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
	key, _, ok := h.resolveKey(r)
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

	// Auth-config catalog, indexed by toolkit slug. Used to resolve each app's
	// auth_config_id (creating a managed config on the fly when missing) and, for
	// the implicit "all apps" case, to enumerate the connected apps.
	authConfigs, err := client.ListAuthConfigs(ctx)
	if err != nil {
		h.logger.Error("composio: list auth configs", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	acBySlug := map[string]string{}
	for _, ac := range authConfigs {
		slug := strings.ToLower(ac.Toolkit.Slug)
		if slug != "" {
			if _, dup := acBySlug[slug]; !dup {
				acBySlug[slug] = ac.ID
			}
		}
	}

	apps := normalizeComposioApps(req, acBySlug)
	if len(apps) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "no apps to bind (connect an app first)")
		return
	}

	// Per-app: resolve auth config + allowed_tools, then find-or-create a
	// scoped Composio MCP server. All external calls happen BEFORE the DB tx so a
	// Composio error never leaves a half-written binding.
	resolved := make([]composioResolvedApp, 0, len(apps))
	for _, app := range apps {
		authID := acBySlug[app.Toolkit]
		if authID == "" {
			id, cerr := client.CreateManagedAuthConfig(ctx, app.Toolkit, app.Toolkit+"-crewship")
			if cerr != nil {
				h.logger.Error("composio: create auth config", "toolkit", app.Toolkit, "error", cerr)
				writeProblem(w, r, http.StatusBadGateway, "Composio API error (auth config)")
				return
			}
			authID = id
			acBySlug[app.Toolkit] = id
		}

		var allowedTools []string
		switch app.Mode {
		case "full":
			allowedTools = nil // empty ⇒ Composio exposes every tool
		case "read":
			tools, terr := client.ListAllTools(ctx, app.Toolkit)
			if terr != nil {
				h.logger.Error("composio: list all tools", "toolkit", app.Toolkit, "error", terr)
				writeProblem(w, r, http.StatusBadGateway, "Composio API error (tools)")
				return
			}
			for _, t := range tools {
				if isReadTool(t.Slug) {
					allowedTools = append(allowedTools, t.Slug)
				}
			}
			if len(allowedTools) == 0 {
				writeProblem(w, r, http.StatusBadRequest, "no tools selected for "+app.Toolkit)
				return
			}
		case "custom":
			tools, terr := client.ListAllTools(ctx, app.Toolkit)
			if terr != nil {
				h.logger.Error("composio: list all tools", "toolkit", app.Toolkit, "error", terr)
				writeProblem(w, r, http.StatusBadGateway, "Composio API error (tools)")
				return
			}
			real := make(map[string]string, len(tools))
			for _, t := range tools {
				real[strings.ToUpper(t.Slug)] = t.Slug
			}
			seen := map[string]struct{}{}
			for _, want := range app.Tools {
				canon, valid := real[strings.ToUpper(strings.TrimSpace(want))]
				if !valid {
					continue // reject bogus slugs rather than over-granting
				}
				if _, dup := seen[canon]; dup {
					continue
				}
				seen[canon] = struct{}{}
				allowedTools = append(allowedTools, canon)
			}
			if len(allowedTools) == 0 {
				writeProblem(w, r, http.StatusBadRequest, "no tools selected for "+app.Toolkit)
				return
			}
		default:
			writeProblem(w, r, http.StatusBadRequest, "invalid mode for "+app.Toolkit+" (use full|read|custom)")
			return
		}

		// One Composio MCP server per (workspace, app, scope), name-shaped to
		// Composio's 30-char limit. Find-or-create by name (a list error is
		// non-fatal — fall through to create).
		scopeTag := composioScopeTag(allowedTools)
		mcpName := composioServerName(wsID, app.Toolkit, scopeTag)
		var composioServerID string
		if servers, lerr := client.ListMCPServers(ctx, mcpName); lerr != nil {
			h.logger.Warn("composio: list mcp servers", "error", lerr)
		} else {
			for _, s := range servers {
				if s.Name == mcpName && s.ID != "" {
					composioServerID = s.ID
					break
				}
			}
		}
		if composioServerID == "" {
			id, _, cerr := client.CreateMCPServer(ctx, mcpName, []string{authID}, allowedTools)
			if cerr != nil {
				h.logger.Error("composio: create mcp server", "error", cerr)
				writeProblem(w, r, http.StatusBadGateway, "Composio API error (mcp server)")
				return
			}
			composioServerID = id
		}
		if composioServerID == "" {
			h.logger.Error("composio: mcp server returned empty id")
			writeProblem(w, r, http.StatusBadGateway, "Composio API error (mcp server)")
			return
		}

		resolved = append(resolved, composioResolvedApp{
			toolkit:   app.Toolkit,
			mode:      app.Mode,
			serverRow: "composio-" + agentID + "-" + app.Toolkit,
			display:   "Composio: " + app.Toolkit + " · " + composioModeLabel(app.Mode, len(allowedTools)),
			// Per-user transport URL — NOT the canonical mcp_url (it 307-redirects
			// to a path the sidecar's MCP client won't follow).
			endpoint: client.MCPUserURL(composioServerID, req.UserID),
		})
	}

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

	// One transaction across all apps: the managed credential (once) + a
	// workspace_mcp_servers row and agent_mcp_bindings row per app, then a sweep
	// that removes the agent's other (de-selected) composio rows.
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.logger.Error("composio: begin bind tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	// (1) Credential — upsert on UNIQUE(workspace_id, name); capture the id of
	// whichever row now holds the key (existing or freshly inserted).
	credID := generateCUID()
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

	// (2)+(3) Per app: workspace_mcp_servers row (upsert) then its agent binding.
	grantedNames := make([]string, 0, len(resolved))
	for _, ra := range resolved {
		serverID := generateCUID()
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
			serverID, wsID, ra.serverRow, ra.display, ra.endpoint, now, now); err != nil {
			h.logger.Error("composio: upsert mcp server row", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND name = ?`, wsID, ra.serverRow).Scan(&serverID); err != nil {
			h.logger.Error("composio: read mcp server id", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		bindingID := generateCUID()
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
		grantedNames = append(grantedNames, ra.serverRow)
	}

	// (4) De-selection sweep: remove the agent's OTHER composio rows + bindings
	// (per-app names "composio-<agentID>-<…>" plus the legacy single-row name
	// "composio-<agentID>"), excluding the granted set. Delete bindings before
	// server rows so the polymorphic-FK trigger stays satisfied.
	staleArgs := []any{wsID, "composio-" + agentID, "composio-" + agentID + "-%"}
	notIn := ""
	if len(grantedNames) > 0 {
		ph := make([]string, len(grantedNames))
		for i, n := range grantedNames {
			ph[i] = "?"
			staleArgs = append(staleArgs, n)
		}
		notIn = " AND name NOT IN (" + strings.Join(ph, ",") + ")"
	}
	staleRows, err := tx.QueryContext(ctx,
		`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND icon = 'composio' AND (name = ? OR name LIKE ?)`+notIn, staleArgs...)
	if err != nil {
		h.logger.Error("composio: query stale servers", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var staleIDs []string
	for staleRows.Next() {
		var id string
		if err := staleRows.Scan(&id); err != nil {
			staleRows.Close()
			h.logger.Error("composio: scan stale server", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		staleIDs = append(staleIDs, id)
	}
	staleRows.Close()
	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_mcp_bindings WHERE agent_id = ? AND mcp_server_id = ?`, agentID, id); err != nil {
			h.logger.Error("composio: delete stale binding", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM workspace_mcp_servers WHERE id = ? AND workspace_id = ?`, id, wsID); err != nil {
			h.logger.Error("composio: delete stale server", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("composio: commit bind", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	respApps := make([]composioBindAppResult, 0, len(resolved))
	for _, ra := range resolved {
		respApps = append(respApps, composioBindAppResult{Toolkit: ra.toolkit, Mode: ra.mode, Endpoint: ra.endpoint})
	}
	writeJSON(w, http.StatusOK, composioBindResponse{
		AgentID: agentID,
		UserID:  req.UserID,
		Apps:    respApps,
	})
}

// UnbindAgent removes an agent's Composio access. With ?toolkit=<slug> it removes
// just that one app's row + binding; without it, it removes ALL of the agent's
// composio rows + bindings. Hard-deletes (bindings first, then server rows, to
// respect the polymorphic-FK trigger). OWNER/ADMIN only.
//
// DELETE /api/v1/integrations/composio/agents/{agentId}/bind[?toolkit=...]
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
	if agentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent id required")
		return
	}
	toolkit := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("toolkit")))

	ctx := r.Context()
	// Resolve the agent's composio server rows (scoped to this workspace so we
	// never touch another workspace's rows). With a toolkit filter, match only
	// that app's row; otherwise every composio row for the agent (per-app names
	// plus the legacy single-row name).
	var (
		rows *sql.Rows
		err  error
	)
	if toolkit != "" {
		rows, err = h.db.QueryContext(ctx,
			`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND icon = 'composio' AND name = ?`,
			wsID, "composio-"+agentID+"-"+toolkit)
	} else {
		rows, err = h.db.QueryContext(ctx,
			`SELECT id FROM workspace_mcp_servers WHERE workspace_id = ? AND icon = 'composio' AND (name = ? OR name LIKE ?)`,
			wsID, "composio-"+agentID, "composio-"+agentID+"-%")
	}
	if err != nil {
		h.logger.Error("composio: lookup unbind servers", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var serverIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			h.logger.Error("composio: scan unbind server", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		serverIDs = append(serverIDs, id)
	}
	rows.Close()
	if len(serverIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.logger.Error("composio: begin unbind tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	for _, id := range serverIDs {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_mcp_bindings WHERE agent_id = ? AND mcp_server_id = ? AND mcp_server_scope = 'workspace'`,
			agentID, id); err != nil {
			h.logger.Error("composio: delete agent binding", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM workspace_mcp_servers WHERE id = ? AND workspace_id = ?`, id, wsID); err != nil {
			h.logger.Error("composio: delete mcp server", "error", err)
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

// composioAgentBinding is one Composio app binding on an agent.
type composioAgentBinding struct {
	Toolkit  string `json:"toolkit"`
	Mode     string `json:"mode"`
	UserID   string `json:"user_id"`
	Endpoint string `json:"endpoint"`
}

type composioListBindingsResponse struct {
	AgentID  string                 `json:"agent_id"`
	Bindings []composioAgentBinding `json:"bindings"`
}

// ListAgentBindings returns the agent's per-app Composio bindings: the toolkit
// (parsed from the server-row name), the scope mode (parsed from the display
// name), the user_id (from the endpoint), and the endpoint. Read-gated.
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

	// Composio-managed servers carry icon 'composio' and per-app names
	// 'composio-<agentID>-<toolkit>' (legacy: 'composio-<agentID>').
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ws.endpoint, ws.name, ws.display_name
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

	prefix := "composio-" + agentID + "-"
	bindings := []composioAgentBinding{}
	for rows.Next() {
		var endpoint, display sql.NullString
		var name string
		if err := rows.Scan(&endpoint, &name, &display); err != nil {
			h.logger.Error("composio: scan agent binding", "error", err)
			continue
		}
		toolkit := ""
		if strings.HasPrefix(name, prefix) {
			toolkit = strings.TrimPrefix(name, prefix)
		}
		bindings = append(bindings, composioAgentBinding{
			Toolkit:  toolkit,
			Mode:     composioModeFromDisplay(display.String),
			UserID:   composioUserIDFromEndpoint(endpoint.String),
			Endpoint: endpoint.String,
		})
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
