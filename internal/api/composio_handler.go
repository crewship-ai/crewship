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
		var enc, baseURL sql.NullString
		err := h.db.QueryRowContext(r.Context(),
			`SELECT encrypted_api_key, base_url FROM composio_settings WHERE workspace_id = ?`, wsID).Scan(&enc, &baseURL)
		switch {
		case err == nil && enc.Valid && enc.String != "":
			key, derr := encryption.Decrypt(enc.String)
			if derr != nil {
				h.logger.Error("composio: decrypt workspace key", "error", derr)
				break
			}
			if key != "" {
				return composio.NewClient(key, baseURL.String), "workspace"
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

// ── Settings (API key) — /api/v1/integrations/composio/settings ──────────────

type composioSettingsResponse struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"` // "workspace" | "env" | "none"
	Label      string `json:"label,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
}

// settingsState reports the effective config without ever returning the key.
func (h *ComposioHandler) settingsState(r *http.Request) composioSettingsResponse {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID != "" {
		var label, baseURL sql.NullString
		err := h.db.QueryRowContext(r.Context(),
			`SELECT label, base_url FROM composio_settings WHERE workspace_id = ?`, wsID).Scan(&label, &baseURL)
		if err == nil {
			return composioSettingsResponse{Configured: true, Source: "workspace", Label: label.String, BaseURL: baseURL.String}
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
	var req struct {
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
		Label   string `json:"label"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	if req.APIKey == "" {
		writeProblem(w, r, http.StatusBadRequest, "api_key is required")
		return
	}

	// Validate against Composio before persisting — a cheap authed call that
	// fails fast on a bad key/project (the UI surfaces this inline).
	probe := composio.NewClient(req.APIKey, req.BaseURL)
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
		wsID, enc, req.BaseURL, req.Label, createdBy, now, now)
	if err != nil {
		h.logger.Error("composio: upsert settings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, composioSettingsResponse{
		Configured: true, Source: "workspace", Label: req.Label, BaseURL: req.BaseURL,
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
