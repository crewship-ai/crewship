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
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewClient builds a Composio client. An empty baseURL falls back to
// DefaultBaseURL; a trailing slash is trimmed so path concatenation is clean.
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
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

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("composio: marshal %s body: %w", path, err)
		}
		reqBody = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
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
