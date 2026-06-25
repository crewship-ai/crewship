// Package composio is a thin client for the Composio managed-integration
// platform (https://composio.dev). Crewship is retiring its self-hosted MCP
// connector management in favour of Composio: instead of standing up and
// babysitting MCP servers, users connect apps (GitHub, Slack, Gmail, …) once
// and Crewship scopes each agent to a specific Composio user (and its
// connected accounts) when generating the agent's MCP URL.
//
// This package owns ONLY the outbound HTTP wire protocol against Composio's
// v3 REST API. Crewship-side concerns (which agent maps to which Composio
// user_id, persistence, RBAC) live in internal/api. Keeping the wire client
// dependency-free makes it trivially unit-testable against an httptest.Server.
//
// Object model recap (see docs/guides/integrations.mdx):
//
//   - Auth Config       — per-toolkit OAuth blueprint, shared by all users.
//   - User (user_id)    — your end-user identity; the isolation boundary.
//   - Connected Account — one user's authorised credentials for a toolkit.
//
// An agent is pointed at exactly one user_id and therefore sees only that
// user's connected accounts — never anyone else's.
package composio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Composio's production API host. Overridable (via
// NewClient) so tests can point at an httptest.Server and operators can pin a
// region/proxy through COMPOSIO_BASE_URL.
const DefaultBaseURL = "https://backend.composio.dev"

// maxResponseBytes caps how much of a Composio response we buffer. The list
// endpoints we call are small; the cap is a defensive bound against a
// misbehaving/proxied endpoint streaming unbounded data into the daemon.
const maxResponseBytes = 8 << 20 // 8 MiB

// Client talks to the Composio v3 REST API with a project-scoped API key.
type Client struct {
	apiKey   string
	baseURL  string
	baseHost string // host of baseURL; every request must stay on it (SSRF guard)
	http     *http.Client
}

// NewClient builds a Composio client. An empty baseURL falls back to
// DefaultBaseURL; a trailing slash is trimmed so path concatenation is clean.
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	trimmed := strings.TrimRight(baseURL, "/")
	host := ""
	if u, err := url.Parse(trimmed); err == nil {
		host = u.Host
	}
	return &Client{
		apiKey:   apiKey,
		baseURL:  trimmed,
		baseHost: host,
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// Toolkit is the app a connector/account belongs to (gmail, github, …).
type Toolkit struct {
	Slug string `json:"slug"`
	Logo string `json:"logo,omitempty"`
}

// AuthConfig is the per-toolkit OAuth blueprint configured in the project.
// It's the "catalog" entry: one per connectable app, shared by all users.
type AuthConfig struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Status  string  `json:"status"`
	Toolkit Toolkit `json:"toolkit"`
}

// ConnectedAccount is one Composio user's authorised connection to a toolkit.
// The UserID is the isolation key Crewship binds agents against.
type ConnectedAccount struct {
	ID         string                     `json:"id"`
	UserID     string                     `json:"user_id"`
	Status     string                     `json:"status"`
	Toolkit    Toolkit                    `json:"toolkit"`
	AuthConfig ConnectedAccountAuthConfig `json:"auth_config"`
}

// ConnectedAccountAuthConfig is the auth-config summary embedded in a
// connected account. IsComposioManaged matters operationally: Composio is
// deprecating initiate() for managed auth configs in favour of Connect Link,
// so the connect flow (slice 2c) keys off this.
type ConnectedAccountAuthConfig struct {
	ID                string `json:"id"`
	AuthScheme        string `json:"auth_scheme"`
	IsComposioManaged bool   `json:"is_composio_managed"`
	IsDisabled        bool   `json:"is_disabled"`
}

// ToolkitCategory tags a toolkit (e.g. "email", "developer tools").
type ToolkitCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ToolkitMeta is the descriptive payload Composio nests under `meta`.
type ToolkitMeta struct {
	Description string            `json:"description"`
	Logo        string            `json:"logo"`
	ToolsCount  int               `json:"tools_count"`
	Categories  []ToolkitCategory `json:"categories"`
}

// ToolkitInfo is one entry in the Composio app catalog (1000+ apps). Distinct
// from the minimal Toolkit embedded in accounts/auth-configs.
type ToolkitInfo struct {
	Slug   string      `json:"slug"`
	Name   string      `json:"name"`
	NoAuth bool        `json:"no_auth"`
	Meta   ToolkitMeta `json:"meta"`
}

// ToolkitPage is a paginated slice of the catalog.
type ToolkitPage struct {
	Items      []ToolkitInfo
	TotalItems int
}

// ListToolkits returns a page of the connector catalog. search/category are
// passed through to Composio (both are server-side filters); limit caps the
// page size (Composio default applies when <= 0).
func (c *Client) ListToolkits(ctx context.Context, search, category string, limit int) (ToolkitPage, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	if category != "" {
		q.Set("category", category)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v3/toolkits"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var env struct {
		Items      []ToolkitInfo `json:"items"`
		TotalItems int           `json:"total_items"`
	}
	if err := c.get(ctx, path, &env); err != nil {
		return ToolkitPage{}, err
	}
	return ToolkitPage{Items: env.Items, TotalItems: env.TotalItems}, nil
}

// Tool is a single action a toolkit exposes (GitHub has 846, Gmail 61, …).
// The Toolkit field carries the parent toolkit slug Composio nests under
// `toolkit`. Distinct from ToolkitInfo (the catalog entry for the whole app).
type Tool struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Toolkit     Toolkit `json:"toolkit"`
}

// ToolPage is a paginated slice of a toolkit's tools.
type ToolPage struct {
	Items      []Tool
	TotalItems int
}

// ListTools returns a page of the tools a toolkit exposes. toolkitSlug filters
// to one app (Composio's `toolkit_slug` query param); search is passed through
// (server-side filter); limit caps the page size (Composio default applies when
// <= 0).
func (c *Client) ListTools(ctx context.Context, toolkitSlug, search string, limit int) (ToolPage, error) {
	q := url.Values{}
	if toolkitSlug != "" {
		q.Set("toolkit_slug", toolkitSlug)
	}
	if search != "" {
		q.Set("search", search)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v3.1/tools"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var env struct {
		Items      []Tool `json:"items"`
		TotalItems int    `json:"total_items"`
	}
	if err := c.get(ctx, path, &env); err != nil {
		return ToolPage{}, err
	}
	return ToolPage{Items: env.Items, TotalItems: env.TotalItems}, nil
}

// TriggerType is one available event subscription a toolkit exposes
// (GMAIL_NEW_MESSAGE, GITHUB_PR_OPENED, …). Type is the delivery mechanism
// Composio uses ("webhook" event-based / "poll" scheduled check). Distinct
// from a TriggerInstance, which is a *live* subscription bound to a user.
type TriggerType struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Type        string  `json:"type"`
	Toolkit     Toolkit `json:"toolkit"`
}

// TriggerTypePage is a paginated slice of a toolkit's trigger types.
type TriggerTypePage struct {
	Items      []TriggerType
	TotalItems int
}

// TriggerInstance is a live trigger subscription bound to one Composio user
// (and its connected account). TriggerName is the underlying trigger-type slug
// Composio nests under `trigger_name`. State carries the subscription's config
// echo / status; DisabledAt is set once a trigger has been turned off.
type TriggerInstance struct {
	ID                 string         `json:"id"`
	TriggerName        string         `json:"trigger_name"`
	UserID             string         `json:"user_id"`
	ConnectedAccountID string         `json:"connected_account_id"`
	TriggerConfig      map[string]any `json:"trigger_config"`
	DisabledAt         string         `json:"disabled_at,omitempty"`
}

// ListTriggerTypes returns a page of the trigger types a toolkit exposes.
// toolkitSlug filters to one app (Composio's `toolkit_slugs` query param, which
// accepts repeated slugs — we pass the single slug); search is passed through
// (server-side filter); limit caps the page size (Composio default applies when
// <= 0).
func (c *Client) ListTriggerTypes(ctx context.Context, toolkitSlug, search string, limit int) (TriggerTypePage, error) {
	q := url.Values{}
	if toolkitSlug != "" {
		q.Set("toolkit_slugs", toolkitSlug)
	}
	if search != "" {
		q.Set("search", search)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/v3.1/triggers_types"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	var env struct {
		Items      []TriggerType `json:"items"`
		TotalItems int           `json:"total_items"`
	}
	if err := c.get(ctx, path, &env); err != nil {
		return TriggerTypePage{}, err
	}
	return TriggerTypePage{Items: env.Items, TotalItems: env.TotalItems}, nil
}

// ListActiveTriggers returns every active trigger instance in the project
// across all users. Grouping/filtering by user happens caller-side, mirroring
// ListConnectedAccounts.
func (c *Client) ListActiveTriggers(ctx context.Context) ([]TriggerInstance, error) {
	var env struct {
		Items []TriggerInstance `json:"items"`
	}
	if err := c.get(ctx, "/api/v3.1/trigger_instances/active", &env); err != nil {
		return nil, err
	}
	return env.Items, nil
}

// CreateTriggerInstance creates (or re-enables) a trigger instance for a user.
// slug is the trigger-type slug (GMAIL_NEW_MESSAGE, …); userID is the Composio
// user that owns the connected account the trigger fires against; config is the
// trigger-type-specific configuration (may be nil/empty). Composio's upsert
// endpoint returns the new trigger's id under `trigger_id`.
func (c *Client) CreateTriggerInstance(ctx context.Context, slug, userID string, config map[string]any) (TriggerInstance, error) {
	body := map[string]any{
		"user_id": userID,
	}
	if config != nil {
		body["trigger_config"] = config
	}
	var out struct {
		TriggerID string `json:"trigger_id"`
	}
	if err := c.post(ctx, "/api/v3.1/trigger_instances/"+url.PathEscape(slug)+"/upsert", body, &out); err != nil {
		return TriggerInstance{}, err
	}
	return TriggerInstance{
		ID:            out.TriggerID,
		TriggerName:   slug,
		UserID:        userID,
		TriggerConfig: config,
	}, nil
}

// ListAuthConfigs returns the project's connector catalog (one entry per
// configured app).
func (c *Client) ListAuthConfigs(ctx context.Context) ([]AuthConfig, error) {
	var env struct {
		Items []AuthConfig `json:"items"`
	}
	if err := c.get(ctx, "/api/v3/auth_configs", &env); err != nil {
		return nil, err
	}
	return env.Items, nil
}

// ListConnectedAccounts returns every connected account in the project across
// all users. Grouping by UserID happens caller-side (the inventory handler).
func (c *Client) ListConnectedAccounts(ctx context.Context) ([]ConnectedAccount, error) {
	var env struct {
		Items []ConnectedAccount `json:"items"`
	}
	if err := c.get(ctx, "/api/v3/connected_accounts", &env); err != nil {
		return nil, err
	}
	return env.Items, nil
}

// RevokeConnectedAccount de-authorizes a connected account at the provider
// (Composio's POST .../revoke). The account row survives but its credentials
// are invalidated upstream — the user must re-connect to use it again. id is
// the account's Composio nanoid. No response body is consumed.
func (c *Client) RevokeConnectedAccount(ctx context.Context, id string) error {
	return c.post(ctx, "/api/v3.1/connected_accounts/"+url.PathEscape(id)+"/revoke", nil, nil)
}

// RefreshConnectedAccount refreshes a connected account's credentials
// (Composio's POST .../refresh), e.g. exchanging a refresh token for a new
// access token. id is the account's Composio nanoid. No response body is
// consumed.
func (c *Client) RefreshConnectedAccount(ctx context.Context, id string) error {
	return c.post(ctx, "/api/v3.1/connected_accounts/"+url.PathEscape(id)+"/refresh", nil, nil)
}

// DeleteConnectedAccount permanently removes a connected account (Composio's
// DELETE on the account resource). id is the account's Composio nanoid. No
// response body is consumed.
func (c *Client) DeleteConnectedAccount(ctx context.Context, id string) error {
	return c.del(ctx, "/api/v3.1/connected_accounts/"+url.PathEscape(id))
}

// ConnectLink is the hosted-auth session returned by CreateConnectLink. The
// caller sends the user to RedirectURL to complete OAuth; the connected
// account lands under the requested user_id when they finish.
type ConnectLink struct {
	LinkToken          string `json:"link_token"`
	RedirectURL        string `json:"redirect_url"`
	ExpiresAt          string `json:"expires_at"`
	ConnectedAccountID string `json:"connected_account_id"`
}

// FindAuthConfig returns the auth config for a toolkit slug, or ("", nil) when
// none exists yet.
func (c *Client) FindAuthConfig(ctx context.Context, toolkitSlug string) (string, error) {
	configs, err := c.ListAuthConfigs(ctx)
	if err != nil {
		return "", err
	}
	for _, ac := range configs {
		if ac.Toolkit.Slug == toolkitSlug {
			return ac.ID, nil
		}
	}
	return "", nil
}

// CreateManagedAuthConfig creates a Composio-managed (no BYO OAuth app) auth
// config for a toolkit and returns its id. Used when connecting an app that
// has no auth config yet.
func (c *Client) CreateManagedAuthConfig(ctx context.Context, toolkitSlug, name string) (string, error) {
	body := map[string]any{
		"toolkit": map[string]string{"slug": toolkitSlug},
		"auth_config": map[string]any{
			"type": "use_composio_managed_auth",
			"name": name,
		},
	}
	var out struct {
		// Composio returns either {auth_config:{id}} or a flat {id} depending
		// on version; accept both.
		ID         string `json:"id"`
		AuthConfig struct {
			ID string `json:"id"`
		} `json:"auth_config"`
	}
	if err := c.post(ctx, "/api/v3.1/auth_configs", body, &out); err != nil {
		return "", err
	}
	if out.AuthConfig.ID != "" {
		return out.AuthConfig.ID, nil
	}
	return out.ID, nil
}

// CreateMCPServer provisions a Composio-managed MCP server scoped to the given
// auth configs and returns its id and base MCP URL. The returned URL is the
// project-wide endpoint; callers append a `user_id` query param to scope it to
// a single Composio user (see internal/api/composio_handler.go BindAgent).
//
// name must be 4–30 chars of [a-zA-Z0-9- ] (Composio rejects anything else with
// a 400); the caller is responsible for shaping it. managed_auth_via_composio is
// always set so the server brokers credentials through Composio rather than
// expecting the caller to forward per-account tokens.
func (c *Client) CreateMCPServer(ctx context.Context, name string, authConfigIDs []string) (string, string, error) {
	body := map[string]any{
		"name":                      name,
		"auth_config_ids":           authConfigIDs,
		"managed_auth_via_composio": true,
	}
	var out struct {
		ID     string `json:"id"`
		MCPURL string `json:"mcp_url"`
	}
	if err := c.post(ctx, "/api/v3.1/mcp/servers", body, &out); err != nil {
		return "", "", err
	}
	return out.ID, out.MCPURL, nil
}

// CreateConnectLink starts a hosted-auth (Connect Link) session for a user
// against an auth config. callbackURL is optional (empty → Composio's hosted
// success page).
func (c *Client) CreateConnectLink(ctx context.Context, authConfigID, userID, callbackURL string) (ConnectLink, error) {
	body := map[string]any{
		"auth_config_id": authConfigID,
		"user_id":        userID,
	}
	if callbackURL != "" {
		body["callback_url"] = callbackURL
	}
	var out ConnectLink
	if err := c.post(ctx, "/api/v3.1/connected_accounts/link", body, &out); err != nil {
		return ConnectLink{}, err
	}
	return out, nil
}

// get performs an authenticated GET and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// post performs an authenticated POST with a JSON body and decodes the response.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// del performs an authenticated DELETE. Composio's delete endpoints return
// 200/204 with no useful body, so nothing is decoded — do treats any 2xx as
// success and skips the decode when out is nil.
func (c *Client) del(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("composio: marshal %s body: %w", path, err)
		}
		reqBody = strings.NewReader(string(b))
	}
	// Resolve + host-pin the URL before issuing the request. Caller-supplied
	// path segments are already url.PathEscape'd and query values go through
	// url.Values, but validating the resolved host against the configured one
	// is the real SSRF guard (and a sanitizer for the "uncontrolled URL" check):
	// no path/query value can redirect the request to a different host.
	u, perr := url.Parse(c.baseURL + path)
	if perr != nil {
		return fmt.Errorf("composio: parse url %s: %w", path, perr)
	}
	if u.Host != c.baseHost {
		return fmt.Errorf("composio: refusing request to unexpected host %q", u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reqBody)
	if err != nil {
		return fmt.Errorf("composio: build request %s: %w", path, err)
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("composio: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	rd := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(rd, 512))
		return fmt.Errorf("composio: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(rd).Decode(out); err != nil {
		return fmt.Errorf("composio: decode %s: %w", path, err)
	}
	return nil
}
