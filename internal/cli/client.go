package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL     string
	Token       string
	WorkspaceID string
	HTTPClient  *http.Client
	Verbose     bool
	// ctx is bound to every request issued by Do. Defaults to
	// context.Background(); use WithContext to attach a cancellable
	// context (e.g., for graceful shutdown via Ctrl-C).
	ctx context.Context
	// resolvedWorkspaceID caches the resolved CUID after first lookup
	resolvedWorkspaceID string
}

func NewClient(baseURL, token, workspaceID string) *Client {
	return &Client{
		BaseURL:     baseURL,
		Token:       token,
		WorkspaceID: workspaceID,
		ctx:         context.Background(),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
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

func (c *Client) Do(method, path string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}

	// Inject workspace_id if set and not already in query
	wsID := c.GetWorkspaceID()
	if wsID != "" {
		q := u.Query()
		if q.Get("workspace_id") == "" {
			q.Set("workspace_id", wsID)
			u.RawQuery = q.Encode()
		}
	}

	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// GetWorkspaceID returns the resolved workspace ID (CUID).
// If WorkspaceID looks like a slug (not a CUID), it resolves it.
func (c *Client) GetWorkspaceID() string {
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
	id, err := c.resolveWorkspaceSlug(c.WorkspaceID)
	if err != nil {
		// Fall back to using the slug directly
		return c.WorkspaceID
	}
	c.resolvedWorkspaceID = id
	return id
}

func (c *Client) resolveWorkspaceSlug(slug string) (string, error) {
	u, err := url.Parse(c.BaseURL + "/api/v1/workspaces")
	if err != nil {
		return "", fmt.Errorf("parse workspace URL: %w", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), "GET", u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create workspace request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
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

func (c *Client) Get(path string) (*http.Response, error) {
	return c.Do("GET", path, nil)
}

func (c *Client) Post(path string, body interface{}) (*http.Response, error) {
	return c.Do("POST", path, body)
}

func (c *Client) Patch(path string, body interface{}) (*http.Response, error) {
	return c.Do("PATCH", path, body)
}

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
