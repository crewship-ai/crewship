package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// MCPGateway manages connections to external MCP servers and routes tool calls
// with transparent per-agent credential injection (Gateway Offload pattern).
type MCPGateway struct {
	mu         sync.RWMutex
	clients    map[string]*mcpClient // keyed by server name
	tools      map[string][]MCPTool  // cached tool catalog per server
	ipc        *IPCConfig
	logger     *slog.Logger
	auditCh    chan auditEntry // bounded audit log channel
	auditHTTP  *http.Client   // dedicated client for audit calls
}

type auditEntry struct {
	serverID    string
	serverScope string
	serverName  string
	toolName    string
	durationMS  int64
	status      string
	errMsg      string
}

// MCPTool represents a tool discovered from an MCP server.
type MCPTool struct {
	ServerName  string      `json:"server_name"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema,omitempty"`
}

// mcpClient manages a connection to a single MCP server.
type mcpClient struct {
	mu          sync.Mutex
	serverID    string
	serverName  string
	displayName string
	transport   string
	endpoint    string
	credential  *MCPCredInput
	sessionID   string // Mcp-Session-Id for streamable-http
	httpClient  *http.Client
	logger      *slog.Logger
	nextID      atomic.Int64 // JSON-RPC request ID counter
}

// MCPCallRequest is the JSON body for /mcp/call.
type MCPCallRequest struct {
	Server string          `json:"server"`
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
}

// MCPCallResponse is the response from /mcp/call.
type MCPCallResponse struct {
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
	IsError bool            `json:"isError,omitempty"`
}

// MCPServerStatus represents the health status of an MCP server connection.
type MCPServerStatus struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Transport   string `json:"transport"`
	Endpoint    string `json:"endpoint"`
	Connected   bool   `json:"connected"`
	ToolCount   int    `json:"tool_count"`
	Error       string `json:"error,omitempty"`
}

// JSON-RPC 2.0 types for MCP protocol
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    interface{}  `json:"capabilities"`
	ServerInfo      *mcpInfo     `json:"serverInfo"`
}

type mcpInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []mcpToolDef `json:"tools"`
}

type mcpToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// NewMCPGateway creates a new MCP gateway with the given server configurations.
func NewMCPGateway(servers []MCPServerInput, ipc *IPCConfig, logger *slog.Logger) *MCPGateway {
	g := &MCPGateway{
		clients:   make(map[string]*mcpClient, len(servers)),
		tools:     make(map[string][]MCPTool),
		ipc:       ipc,
		logger:    logger,
		auditCh:   make(chan auditEntry, 64),
		auditHTTP: &http.Client{Timeout: 5 * time.Second},
	}
	// Start single audit worker goroutine (bounded)
	go g.auditWorker()

	for _, s := range servers {
		if s.Transport != "streamable-http" {
			logger.Warn("MCP server transport not supported yet, skipping", "name", s.Name, "transport", s.Transport)
			continue
		}
		if s.Endpoint == "" {
			logger.Warn("MCP server has no endpoint, skipping", "name", s.Name)
			continue
		}
		g.clients[s.Name] = &mcpClient{
			serverID:    s.ID,
			serverName:  s.Name,
			displayName: s.DisplayName,
			transport:   s.Transport,
			endpoint:    s.Endpoint,
			credential:  s.Credential,
			httpClient: &http.Client{
				Timeout: 30 * time.Second,
			},
			logger: logger,
		}
	}

	return g
}

// Connect initializes connections to all configured MCP servers.
func (g *MCPGateway) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var lastErr error
	for name, client := range g.clients {
		if err := client.initialize(ctx); err != nil {
			g.logger.Error("MCP server init failed", "name", name, "error", err)
			lastErr = err
			continue
		}
		g.logger.Info("MCP server connected", "name", name)
	}
	return lastErr
}

// DiscoverTools fetches the tool catalog from all connected MCP servers.
func (g *MCPGateway) DiscoverTools(ctx context.Context) ([]MCPTool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var allTools []MCPTool
	for name, client := range g.clients {
		if client.sessionID == "" {
			continue // not connected
		}
		tools, err := client.listTools(ctx)
		if err != nil {
			g.logger.Error("MCP tools/list failed", "name", name, "error", err)
			continue
		}
		var serverTools []MCPTool
		for _, t := range tools {
			serverTools = append(serverTools, MCPTool{
				ServerName:  name,
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
		g.tools[name] = serverTools
		allTools = append(allTools, serverTools...)
	}
	return allTools, nil
}

// ListTools returns the cached tool catalog.
func (g *MCPGateway) ListTools() []MCPTool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var all []MCPTool
	for _, tools := range g.tools {
		all = append(all, tools...)
	}
	if all == nil {
		all = []MCPTool{}
	}
	return all
}

// CallTool routes a tool call to the correct MCP server with credential injection.
func (g *MCPGateway) CallTool(ctx context.Context, serverName, toolName string, input json.RawMessage) (*MCPCallResponse, error) {
	g.mu.RLock()
	client, ok := g.clients[serverName]
	g.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("MCP server %q not found", serverName)
	}
	client.mu.Lock()
	connected := client.sessionID != ""
	client.mu.Unlock()
	if !connected {
		return nil, fmt.Errorf("MCP server %q not connected", serverName)
	}

	start := time.Now()
	result, err := client.callTool(ctx, toolName, input)
	duration := time.Since(start)

	// Audit logging via bounded channel (non-blocking)
	if g.ipc != nil {
		status := "success"
		errMsg := ""
		if err != nil {
			status = "error"
			errMsg = err.Error()
		}
		select {
		case g.auditCh <- auditEntry{
			serverID: client.serverID, serverScope: "workspace", serverName: serverName,
			toolName: toolName, durationMS: duration.Milliseconds(), status: status, errMsg: errMsg,
		}:
		default:
			g.logger.Warn("MCP audit channel full, dropping entry", "tool", toolName)
		}
	}

	if err != nil {
		return &MCPCallResponse{Error: err.Error(), IsError: true}, nil
	}

	return result, nil
}

// Status returns the health status of all MCP server connections.
func (g *MCPGateway) Status() []MCPServerStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var statuses []MCPServerStatus
	for _, client := range g.clients {
		toolCount := len(g.tools[client.serverName])
		client.mu.Lock()
		connected := client.sessionID != ""
		client.mu.Unlock()
		statuses = append(statuses, MCPServerStatus{
			Name:        client.serverName,
			DisplayName: client.displayName,
			Transport:   client.transport,
			Endpoint:    client.endpoint,
			Connected:   connected,
			ToolCount:   toolCount,
		})
	}
	if statuses == nil {
		statuses = []MCPServerStatus{}
	}
	return statuses
}

// Close terminates all MCP server sessions and stops the audit worker.
func (g *MCPGateway) Close() {
	// Close audit channel to stop worker goroutine
	close(g.auditCh)

	g.mu.Lock()
	defer g.mu.Unlock()

	for name, client := range g.clients {
		client.mu.Lock()
		hasSession := client.sessionID != ""
		client.mu.Unlock()
		if hasSession {
			client.terminateSession()
			g.logger.Info("MCP session terminated", "name", name)
		}
	}
}

// --- mcpClient methods ---

// initialize performs the MCP initialize handshake (streamable-http).
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
	if c.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", c.sessionID)
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
	if sid != "" {
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse JSON-RPC response: %w", err)
	}
	return &rpcResp, nil
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
		req.SetBasicAuth("", c.credential.Token)
	}
}

// terminateSession sends a DELETE request to end the MCP session.
func (c *mcpClient) terminateSession() {
	c.mu.Lock()
	sid := c.sessionID
	c.sessionID = ""
	c.mu.Unlock()

	if sid == "" {
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
func (g *MCPGateway) auditWorker() {
	for entry := range g.auditCh {
		g.sendAuditEntry(entry)
	}
}

func (g *MCPGateway) sendAuditEntry(entry auditEntry) {
	if g.ipc == nil || g.ipc.BaseURL == "" {
		return
	}

	payload := map[string]interface{}{
		"workspace_id":     g.ipc.WorkspaceID,
		"agent_id":         g.ipc.AgentID,
		"crew_id":          g.ipc.CrewID,
		"mcp_server_id":    entry.serverID,
		"mcp_server_scope": entry.serverScope,
		"tool_name":        entry.toolName,
		"status":           entry.status,
		"duration_ms":      entry.durationMS,
		"error_message":    entry.errMsg,
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := g.ipc.BaseURL + "/api/v1/internal/mcp-tool-calls"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if req != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", g.ipc.Token)
		resp, err := g.auditHTTP.Do(req)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
}

// --- Sidecar HTTP handlers ---

func (s *Server) handleMCPListTools(w http.ResponseWriter, r *http.Request) {
	if s.mcpGateway == nil {
		writeJSONResponse(w, http.StatusOK, []MCPTool{})
		return
	}
	writeJSONResponse(w, http.StatusOK, s.mcpGateway.ListTools())
}

func (s *Server) handleMCPCallTool(w http.ResponseWriter, r *http.Request) {
	if s.mcpGateway == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "MCP gateway not configured"})
		return
	}

	var req MCPCallRequest
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "Failed to read request"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if req.Server == "" || req.Tool == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "server and tool are required"})
		return
	}

	result, err := s.mcpGateway.CallTool(r.Context(), req.Server, req.Tool, req.Input)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSONResponse(w, http.StatusOK, result)
}

func (s *Server) handleMCPStatus(w http.ResponseWriter, r *http.Request) {
	if s.mcpGateway == nil {
		writeJSONResponse(w, http.StatusOK, map[string]interface{}{
			"enabled": false, "servers": []MCPServerStatus{},
		})
		return
	}
	writeJSONResponse(w, http.StatusOK, map[string]interface{}{
		"enabled": true, "servers": s.mcpGateway.Status(),
	})
}
