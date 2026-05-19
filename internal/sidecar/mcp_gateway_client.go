package sidecar

// Per-server mcpClient internals — JSON-RPC initialize / list_tools /
// call_tool, the underlying sendRequest pump, SSE response parsing,
// per-call credential injection, and session termination. Extracted
// from mcp_gateway.go for readability; the gateway calls these
// methods on its mcpClient instances.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *mcpClient) initialize(ctx context.Context) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "crewship-sidecar",
				"version": "1.0.0",
			},
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	// SSE servers may not return Mcp-Session-Id; use a synthetic marker
	// so the gateway treats the client as connected.
	c.mu.Lock()
	if c.sessionID == "" && c.transport == "sse" {
		c.sessionID = "sse-connected"
	}
	c.mu.Unlock()

	// Send initialized notification
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	// Notifications don't have an ID
	body, _ := json.Marshal(notif)
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" && sid != "sse-connected" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	c.injectCredential(httpReq)
	notifResp, err := c.httpClient.Do(httpReq)
	if err == nil {
		io.Copy(io.Discard, notifResp.Body)
		notifResp.Body.Close()
	}

	return nil
}

// listTools calls tools/list on the MCP server.

func (c *mcpClient) listTools(ctx context.Context) ([]mcpToolDef, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "tools/list",
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result toolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	return result.Tools, nil
}

// callTool calls tools/call on the MCP server.

func (c *mcpClient) callTool(ctx context.Context, toolName string, input json.RawMessage) (*MCPCallResponse, error) {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: toolCallParams{
			Name:      toolName,
			Arguments: input,
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tools/call: %w", err)
	}
	if resp.Error != nil {
		return &MCPCallResponse{
			Error:   resp.Error.Message,
			IsError: true,
		}, nil
	}

	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return &MCPCallResponse{Content: resp.Result}, nil
	}

	content, _ := json.Marshal(result.Content)
	return &MCPCallResponse{
		Content: content,
		IsError: result.IsError,
	}, nil
}

// sendRequest sends a JSON-RPC request via streamable-http and parses the response.

func (c *mcpClient) sendRequest(ctx context.Context, rpcReq jsonRPCRequest) (*jsonRPCResponse, error) {
	// Use atomic counter for unique JSON-RPC IDs
	rpcReq.ID = int(c.nextID.Add(1))

	body, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" && sid != "sse-connected" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	c.injectCredential(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Capture session ID from response (thread-safe)
	if newSID := resp.Header.Get("Mcp-Session-Id"); newSID != "" {
		c.mu.Lock()
		c.sessionID = newSID
		c.mu.Unlock()
	}

	if resp.StatusCode != http.StatusOK {
		// Sanitize: do not expose upstream response body (may contain credential echoes)
		return nil, fmt.Errorf("MCP server %q returned HTTP %d", c.serverName, resp.StatusCode)
	}

	// Handle SSE (text/event-stream) responses from legacy MCP servers.
	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		return parseSSEResponse(resp.Body)
	}

	// Bound the read: MCP tool responses can legitimately be large (file
	// contents, large query results) but a runaway server must not be able
	// to OOM us. 32 MiB covers any realistic tool output.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC response: %w", err)
	}
	return &rpcResp, nil
}

// tryParseSSEEvent attempts to parse a collected SSE event as a JSON-RPC response.
// Returns nil, nil if the event type is not a message or there are no data lines.

func tryParseSSEEvent(eventType string, dataLines []string) (*jsonRPCResponse, error) {
	if (eventType == "" || eventType == "message") && len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		var rpcResp jsonRPCResponse
		if err := json.Unmarshal([]byte(data), &rpcResp); err != nil {
			return nil, fmt.Errorf("parse SSE JSON-RPC data: %w", err)
		}
		return &rpcResp, nil
	}
	return nil, nil
}

// parseSSEResponse reads a text/event-stream body and extracts the first
// JSON-RPC response from a "message" event's data field. This supports
// the deprecated MCP SSE transport where responses arrive as SSE events.

func parseSSEResponse(body io.Reader) (*jsonRPCResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1 MB max token size for large SSE frames
	var eventType string
	var dataLines []string

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// End of event
			if resp, err := tryParseSSEEvent(eventType, dataLines); resp != nil || err != nil {
				return resp, err
			}
			// Reset for next event
			eventType = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE stream: %w", err)
	}

	// Handle final event without trailing blank line
	if resp, err := tryParseSSEEvent(eventType, dataLines); resp != nil || err != nil {
		return resp, err
	}

	return nil, fmt.Errorf("no JSON-RPC message event found in SSE stream")
}

// injectCredential adds authentication headers to the request (Gateway Offload pattern).

func (c *mcpClient) injectCredential(req *http.Request) {
	if c.credential == nil {
		return
	}
	switch c.credential.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+c.credential.Token)
	case "api_key":
		header := c.credential.Header
		if header == "" {
			header = "X-API-Key"
		}
		req.Header.Set(header, c.credential.Token)
	case "basic":
		// Token is used as the password with an empty username.
		// Future: add Username field to MCPCredInput for user:pass servers.
		req.SetBasicAuth("", c.credential.Token)
	}
}

// terminateSession sends a DELETE request to end the MCP session.
// SSE transport does not support session termination via DELETE.

func (c *mcpClient) terminateSession() {
	c.mu.Lock()
	sid := c.sessionID
	c.sessionID = ""
	c.mu.Unlock()

	if sid == "" || sid == "sse-connected" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.endpoint, nil)
	if req != nil {
		req.Header.Set("Mcp-Session-Id", sid)
		c.injectCredential(req)
		resp, err := c.httpClient.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

// auditWorker processes audit entries from the bounded channel.
