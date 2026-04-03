package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		 WHERE id = ? AND crew_id = ?`,
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

// testStreamableHTTPConnection sends a JSON-RPC initialize request to the MCP server endpoint.
func testStreamableHTTPConnection(ctx context.Context, endpoint string) testConnectionResponse {
	if endpoint == "" {
		return testConnectionResponse{
			Status:  "error",
			Message: "No endpoint configured for this server",
		}
	}

	// Build JSON-RPC initialize request per MCP protocol
	initReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":   map[string]interface{}{},
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

	client := &http.Client{Timeout: 10 * time.Second}
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
		// SSE response or non-standard JSON — still consider it OK if 2xx
		return testConnectionResponse{
			Status:  "ok",
			Message: "Server responded successfully",
		}
	default:
		return testConnectionResponse{
			Status:  "error",
			Message: fmt.Sprintf("Server returned HTTP %d", resp.StatusCode),
		}
	}
}
