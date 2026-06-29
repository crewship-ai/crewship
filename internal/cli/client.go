package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Client is the HTTP client for the crewship API, used by the CLI.
type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTPClient  *http.Client
	Verbose     bool
	// TokenHost is the hostname the stored Token was issued for (the host
	// of the configured server URL). When set, the client refuses to attach
	// the bearer token to a request whose host differs — this is the guard
	// against issue #571, where `crewship --server http://attacker.com …`
	// leaked the operator's token to any attacker-controlled host. Empty
	// disables the check (e.g. login flows that mint a token for a brand-new
	// server, or a hand-edited config with a token but no server).
	TokenHost string
	// AllowHostMismatch, when true, sends the token regardless of TokenHost.
	// The intentional escape hatch for SSH tunnels / a moved server, wired
	// from --server-allow-mismatch / CREWSHIP_ALLOW_SERVER_MISMATCH.
	AllowHostMismatch bool
	// ctx is bound to every request issued by Do. Defaults to
	// context.Background(); use WithContext to attach a cancellable
	// context (e.g., for graceful shutdown via Ctrl-C).
	ctx context.Context
	// resolvedWorkspaceID caches the resolved CUID after first lookup
	resolvedWorkspaceID string
}

// DefaultTimeout is the per-request cap for ordinary CLI calls. Overridable via
// CREWSHIP_HTTP_TIMEOUT (seconds) for environments where even routine listing is
// slow. Long synchronous calls (a routine /run that waits for the agent + any
// grader loop) should instead use WithTimeout to lift the cap just for that call.
func defaultHTTPTimeout() time.Duration {
	if v := os.Getenv("CREWSHIP_HTTP_TIMEOUT"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 30 * time.Second
}

// NewClient creates a CLI client targeting the given server URL with
// optional JWT token and workspace ID.
func NewClient(baseURL, token, workspaceID string) *Client {
	return &Client{
		BaseURL:     baseURL,
		Token:       token,
		WorkspaceID: workspaceID,
		ctx:         context.Background(),
		HTTPClient: &http.Client{
			Timeout: defaultHTTPTimeout(),
		},
	}
}

// WithTimeout returns a shallow copy of the client whose HTTP client uses the
// given overall request timeout instead of the default. Use it for endpoints
// that legitimately run long: a synchronous routine /run blocks until the agent
// (and any grader loop) finishes, which routinely exceeds the 30s default and
// would otherwise fail with "context deadline exceeded" even though the
// server-side run completes. A non-positive d leaves the default in place.
func (c *Client) WithTimeout(d time.Duration) *Client {
	if d <= 0 {
		return c
	}
	clone := *c
	hc := *c.HTTPClient
	hc.Timeout = d
	clone.HTTPClient = &hc
	return &clone
}

// WithContext returns a shallow copy of the client whose outgoing requests
// are bound to ctx. A nil ctx falls back to context.Background().
// Use this from command entrypoints so Ctrl-C interrupts in-flight HTTP
// calls instead of waiting for the 30 s client timeout.
func (c *Client) WithContext(ctx context.Context) *Client {
	if ctx == nil {
		ctx = context.Background()
	}
	clone := *c
	clone.ctx = ctx
	return &clone
}

// NewRequest builds an *http.Request targeting path (relative to BaseURL),
// injects the workspace_id query parameter the same way Do does, and applies
// the bearer token via applyAuth — so the issue #571 token-host guard runs for
// EVERY request, including the streaming / multipart / raw-byte paths that
// build their own requests instead of going through Do. body may be nil; the
// caller is responsible for setting Content-Type (Do sets application/json for
// its JSON bodies).
//
// On a host mismatch applyAuth returns a *ServerMismatchError and NewRequest
// returns it (with no request), so the credential is never written to a request
// bound for the wrong host. A nil ctx falls back to context.Background().
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	// Inject workspace_id if set and not already in query. Resolve the slug
	// using ctx (so any preflight lookup honours its cancellation) while still
	// caching the result on c — using a WithContext clone here would discard
	// that cache and re-resolve on every request.
	if wsID := c.getWorkspaceID(ctx); wsID != "" {
		q := u.Query()
		if q.Get("workspace_id") == "" {
			q.Set("workspace_id", wsID)
			u.RawQuery = q.Encode()
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if err := c.applyAuth(req); err != nil {
		return nil, err
	}
	return req, nil
}

// Do sends an HTTP request with the configured auth token and workspace ID.
func (c *Client) Do(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		// io.Reader bodies pass through verbatim — callers that
		// pre-serialised JSON used to silently get json.Marshal'd
		// again (producing `{}` for *bytes.Reader since it has no
		// exported fields), so the server saw an empty body. The
		// io.Reader fast-path makes that pattern do the right thing.
		if r, ok := body.(io.Reader); ok {
			bodyReader = r
		} else {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("marshal body: %w", err)
			}
			bodyReader = bytes.NewReader(data)
		}
	}

	req, err := c.NewRequest(c.ctx, method, path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// ServerMismatchError is returned when the client is asked to send the
// bearer token to a host other than the one the token was issued for.
// The token is NEVER written to the request when this fires — the request
// is refused before it reaches the network (issue #571).
type ServerMismatchError struct {
	TokenHost   string // host the stored token belongs to
	RequestHost string // host the request was about to hit
}

func (e *ServerMismatchError) Error() string {
	return fmt.Sprintf(
		"refusing to send your auth token to %q: the stored credential was issued for %q. "+
			"If this is intentional (SSH tunnel, the server moved), re-run `crewship login --server <url>` "+
			"to rebind the token, or pass --server-allow-mismatch (env CREWSHIP_ALLOW_SERVER_MISMATCH=1)",
		e.RequestHost, e.TokenHost)
}

// applyAuth attaches the bearer token to req after verifying the
// destination host is allowed to receive it. With no token it is a no-op.
// When TokenHost is set and the request host differs (and the mismatch
// override is off), it returns a *ServerMismatchError WITHOUT setting the
// Authorization header, so the credential never rides a request to the
// wrong host.
func (c *Client) applyAuth(req *http.Request) error {
	if c.Token == "" {
		return nil
	}
	if c.TokenHost != "" && !c.AllowHostMismatch {
		reqHost := strings.ToLower(req.URL.Hostname())
		if reqHost != strings.ToLower(c.TokenHost) {
			return &ServerMismatchError{TokenHost: c.TokenHost, RequestHost: reqHost}
		}
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return nil
}

// GetWorkspaceID returns the resolved workspace ID (CUID).
// If WorkspaceID looks like a slug (not a CUID), it resolves it.
func (c *Client) GetWorkspaceID() string {
	return c.getWorkspaceID(c.ctx)
}

// getWorkspaceID is GetWorkspaceID with an explicit context for the slug-
// resolution preflight, so callers (e.g. NewRequest / StreamSSE) can bind it to
// their own cancellation while the resolved CUID is still cached on c.
func (c *Client) getWorkspaceID(ctx context.Context) string {
	if c.WorkspaceID == "" {
		return ""
	}
	if c.resolvedWorkspaceID != "" {
		return c.resolvedWorkspaceID
	}
	// If it already looks like a CUID (starts with 'c', length >= 20), use directly
	if looksLikeCUID(c.WorkspaceID) {
		c.resolvedWorkspaceID = c.WorkspaceID
		return c.WorkspaceID
	}
	// Resolve slug to ID by calling workspaces list (without workspace_id param)
	id, err := c.resolveWorkspaceSlug(ctx, c.WorkspaceID)
	if err != nil {
		// Fall back to using the slug directly
		return c.WorkspaceID
	}
	c.resolvedWorkspaceID = id
	return id
}

func (c *Client) resolveWorkspaceSlug(ctx context.Context, slug string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(c.BaseURL + "/api/v1/workspaces")
	if err != nil {
		return "", fmt.Errorf("parse workspace URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create workspace request: %w", err)
	}
	if err := c.applyAuth(req); err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch workspaces: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("workspace list failed (HTTP %d)", resp.StatusCode)
	}
	var workspaces []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read workspace response: %w", err)
	}
	if err := json.Unmarshal(data, &workspaces); err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		if ws.Slug == slug {
			return ws.ID, nil
		}
	}
	return "", fmt.Errorf("workspace not found: %s", slug)
}

// Get sends an HTTP GET request to the given API path.
func (c *Client) Get(path string) (*http.Response, error) {
	return c.Do("GET", path, nil)
}

// Post sends an HTTP POST request to the given API path with a JSON body.
func (c *Client) Post(path string, body interface{}) (*http.Response, error) {
	return c.Do("POST", path, body)
}

// Patch sends an HTTP PATCH request to the given API path with a JSON body.
func (c *Client) Patch(path string, body interface{}) (*http.Response, error) {
	return c.Do("PATCH", path, body)
}

// Put sends an HTTP PUT request to the given API path with a JSON body.
// Used by full-replacement endpoints like the PR-B per-crew policy PUT
// (PATCH semantics don't apply — every field gets written as an atomic
// snapshot of the new policy + audit triple).
func (c *Client) Put(path string, body interface{}) (*http.Response, error) {
	return c.Do("PUT", path, body)
}

// Delete sends an HTTP DELETE request to the given API path.
func (c *Client) Delete(path string) (*http.Response, error) {
	return c.Do("DELETE", path, nil)
}

// ReadJSON decodes a JSON response body into v and closes the body.
func ReadJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// CheckError reads the body on non-2xx and returns a formatted error.
func CheckError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var errBody struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &errBody) == nil && errBody.Error != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, errBody.Error)
	}

	return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(data))
}

// looksLikeCUID returns true if s looks like a CUID (starts with 'c', alphanumeric, length >= 20).
func looksLikeCUID(s string) bool {
	if len(s) < 20 || s[0] != 'c' {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
