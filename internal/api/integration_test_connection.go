package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// testConnectionResponse is the response body for the test connection endpoint.
type testConnectionResponse struct {
	Status     string          `json:"status"`                // "ok", "error", "auth_required", "skipped"
	Message    string          `json:"message,omitempty"`     // Human-readable message
	ServerInfo json.RawMessage `json:"server_info,omitempty"` // Server capabilities from initialize response
}

// loadAndTestConnection loads transport and endpoint from the database using the
// given query, then performs an MCP connection test and writes the result.
func (h *IntegrationHandler) loadAndTestConnection(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	var transport string
	var endpoint sql.NullString
	err := h.db.QueryRowContext(r.Context(), query, args...).Scan(&transport, &endpoint)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Integration not found"})
		return
	}
	result := testMCPConnection(r.Context(), transport, endpoint.String, h.logger)
	writeJSON(w, http.StatusOK, result)
}

// TestWorkspaceIntegrationConnection tests connectivity to a workspace-level MCP server.
// POST /api/v1/integrations/{integrationId}/test
func (h *IntegrationHandler) TestWorkspaceIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	integrationID := r.PathValue("integrationId")

	h.loadAndTestConnection(w, r,
		`SELECT transport, endpoint FROM workspace_mcp_servers
		 WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		integrationID, workspaceID)
}

// TestCrewIntegrationConnection tests connectivity to a crew-level MCP server.
// POST /api/v1/crews/{crewId}/integrations/{integrationId}/test
func (h *IntegrationHandler) TestCrewIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	integrationID := r.PathValue("integrationId")

	// Verify crew belongs to workspace
	var crewExists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ?",
		crewID, workspaceID).Scan(&crewExists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}

	h.loadAndTestConnection(w, r,
		`SELECT transport, endpoint FROM crew_mcp_servers
		 WHERE id = ? AND crew_id = ? AND deleted_at IS NULL`,
		integrationID, crewID)
}

// testMCPConnection performs the actual connectivity test based on transport type.
func testMCPConnection(ctx context.Context, transport, endpoint string, logger interface{ Error(string, ...any) }) testConnectionResponse {
	switch transport {
	case "stdio":
		return testConnectionResponse{
			Status:  "skipped",
			Message: "Stdio servers are tested at runtime inside the container",
		}
	case "streamable-http", "http", "sse":
		return testStreamableHTTPConnection(ctx, endpoint)
	default:
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Unknown transport type: %s", transport),
		}
	}
}

// isPrivateIP returns true if the given IP belongs to a private, loopback,
// link-local, or otherwise non-routable address range.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []net.IPNet{
		{IP: net.ParseIP("10.0.0.0"), Mask: net.CIDRMask(8, 32)},
		{IP: net.ParseIP("172.16.0.0"), Mask: net.CIDRMask(12, 32)},
		{IP: net.ParseIP("192.168.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("127.0.0.0"), Mask: net.CIDRMask(8, 32)},
		{IP: net.ParseIP("169.254.0.0"), Mask: net.CIDRMask(16, 32)},
		{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)},
	}
	for _, cidr := range privateRanges {
		if cidr.Contains(ip) {
			return true
		}
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// ssrfSafeTransport returns an http.Transport with a custom DialContext that
// validates the resolved IP at connection time, preventing both SSRF and DNS
// rebinding (TOCTOU) attacks.
func ssrfSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS resolution failed for %s: %w", host, err)
			}
			for _, ipAddr := range ips {
				if isPrivateIP(ipAddr.IP) {
					return nil, fmt.Errorf("blocked connection to private/internal IP %s", ipAddr.IP)
				}
			}
			// Connect to the first resolved IP directly to prevent re-resolution.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
}

// looksLikeSSE checks whether the response body contains SSE framing (event: or data: lines).
func looksLikeSSE(body []byte) bool {
	limit := 4096
	if len(body) < limit {
		limit = len(body)
	}
	snippet := string(body[:limit])
	return strings.Contains(snippet, "event:") || strings.Contains(snippet, "data:")
}

// testStreamableHTTPConnection sends a JSON-RPC initialize request to the MCP server endpoint.
func testStreamableHTTPConnection(ctx context.Context, endpoint string) testConnectionResponse {
	if endpoint == "" {
		return testConnectionResponse{
			Status:  "error",
			Message: "No endpoint configured for this server",
		}
	}

	// Validate URL structure before making any network call.
	if _, err := url.Parse(endpoint); err != nil {
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Invalid endpoint URL: %s", err.Error()),
		}
	}

	// Build JSON-RPC initialize request per MCP protocol
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "crewship-test",
				"version": "1.0.0",
			},
		},
	}

	body, err := json.Marshal(initReq)
	if err != nil {
		return testConnectionResponse{
			Status:  "error",
			Message: "Failed to build test request",
		}
	}

	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(testCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Invalid endpoint URL: %s", err.Error()),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	// Use SSRF-safe transport that validates resolved IPs at connection time,
	// preventing DNS rebinding (TOCTOU) attacks.
	client := &http.Client{Timeout: 10 * time.Second, Transport: ssrfSafeTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Connection failed: %s", err.Error()),
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // limit to 64KB

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return testConnectionResponse{
			Status:  "auth_required",
			Message: "Server requires authentication",
		}
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		contentType := resp.Header.Get("Content-Type")

		// Try to parse as JSON-RPC response to extract server info
		var rpcResp struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(respBody, &rpcResp); err == nil && rpcResp.Result != nil {
			return testConnectionResponse{
				Status:     "ok",
				Message:    "Server responded successfully",
				ServerInfo: rpcResp.Result,
			}
		}

		// SSE response: validate Content-Type or presence of SSE framing,
		// and check for JSON-RPC errors in the SSE data.
		if strings.Contains(contentType, "text/event-stream") || looksLikeSSE(respBody) {
			// Try to parse SSE data lines for JSON-RPC error responses
			var sseRPC struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			for _, line := range strings.Split(string(respBody), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "data:") {
					data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
					if json.Unmarshal([]byte(data), &sseRPC) == nil && sseRPC.Error != nil {
						return testConnectionResponse{
							Status:  "error",
							Message: fmt.Sprintf("Server returned JSON-RPC error: %s", sseRPC.Error.Message),
						}
					}
				}
			}
			return testConnectionResponse{
				Status:  "ok",
				Message: "Server responded with SSE stream",
			}
		}

		// Non-JSON, non-SSE 2xx — not a valid MCP server response
		return testConnectionResponse{
			Status:  "error",
			Message: "Server returned 2xx but response is not valid JSON-RPC or SSE",
		}
	default:
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Server returned HTTP %d", resp.StatusCode),
		}
	}
}
