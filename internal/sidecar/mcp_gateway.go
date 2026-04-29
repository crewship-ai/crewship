package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// MCPGateway manages connections to external MCP servers and routes tool calls
// with transparent per-agent credential injection (Gateway Offload pattern).
type MCPGateway struct {
	mu           sync.RWMutex
	clients      map[string]*mcpClient // keyed by server name
	tools        map[string][]MCPTool  // cached tool catalog per server
	ipc          *IPCConfig
	logger       *slog.Logger
	auditCh      chan auditEntry // bounded audit log channel
	auditHTTP    *http.Client    // dedicated client for audit calls
	auditBaseURL string          // base URL for audit HTTP calls
	auditDone    chan struct{}   // closed when audit worker exits
	shutdownCh   chan struct{}   // closed on shutdown to prevent send-to-closed-channel panic
	closeOnce    sync.Once
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
	serverScope string
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
	ProtocolVersion string      `json:"protocolVersion"`
	Capabilities    interface{} `json:"capabilities"`
	ServerInfo      *mcpInfo    `json:"serverInfo"`
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
	// Audit HTTP client uses TCP to crewshipd by default.
	// If IPC BaseURL is a Unix socket path, use UDS transport.
	auditHTTP := &http.Client{Timeout: 5 * time.Second}
	auditBaseURL := ""
	if ipc != nil && ipc.BaseURL != "" {
		if ipc.BaseURL[0] == '/' {
			// Unix socket: use placeholder HTTP host, dial the socket
			socketPath := ipc.BaseURL
			auditBaseURL = "http://localhost"
			auditHTTP.Transport = &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			}
		} else {
			auditBaseURL = ipc.BaseURL
		}
	}

	g := &MCPGateway{
		clients:      make(map[string]*mcpClient, len(servers)),
		tools:        make(map[string][]MCPTool),
		ipc:          ipc,
		logger:       logger,
		auditCh:      make(chan auditEntry, 64),
		auditHTTP:    auditHTTP,
		auditBaseURL: auditBaseURL,
		auditDone:    make(chan struct{}),
		shutdownCh:   make(chan struct{}),
	}
	// Start single audit worker goroutine (bounded)
	go g.auditWorker()

	for _, s := range servers {
		if s.Transport != "streamable-http" && s.Transport != "sse" {
			logger.Debug("MCP server transport not supported by gateway, skipping", "name", s.Name, "transport", s.Transport)
			continue
		}
		if s.Endpoint == "" {
			logger.Warn("MCP server has no endpoint, skipping", "name", s.Name)
			continue
		}
		scope := s.Scope
		if scope == "" {
			scope = "workspace"
		}
		g.clients[s.Name] = &mcpClient{
			serverID:    s.ID,
			serverName:  s.Name,
			serverScope: scope,
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
		client.mu.Lock()
		connected := client.sessionID != ""
		client.mu.Unlock()
		if !connected {
			continue
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

	// Audit logging via bounded channel (non-blocking, shutdown-safe)
	if g.ipc != nil {
		status := "success"
		errMsg := ""
		if err != nil {
			status = "error"
			errMsg = err.Error()
		} else if result != nil && result.IsError {
			status = "error"
			errMsg = result.Error
		}
		select {
		case <-g.shutdownCh:
			// shutting down, skip audit
		default:
			select {
			case g.auditCh <- auditEntry{
				serverID: client.serverID, serverScope: client.serverScope, serverName: serverName,
				toolName: toolName, durationMS: duration.Milliseconds(), status: status, errMsg: errMsg,
			}:
			default:
				g.logger.Warn("MCP audit channel full, dropping entry", "tool", toolName)
			}
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

// Close terminates all MCP server sessions and waits for audit worker to drain.

func (g *MCPGateway) Close() {
	g.closeOnce.Do(func() {
		close(g.shutdownCh) // signal CallTool to stop enqueueing
		close(g.auditCh)    // signal worker to drain and exit
	})
	<-g.auditDone

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

// initialize performs the MCP initialize handshake (streamable-http or sse).

func (g *MCPGateway) auditWorker() {
	defer close(g.auditDone)
	for entry := range g.auditCh {
		g.sendAuditEntry(entry)
	}
}

func (g *MCPGateway) sendAuditEntry(entry auditEntry) {
	if g.ipc == nil || g.auditBaseURL == "" {
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

	url := g.auditBaseURL + "/api/v1/internal/mcp-tool-calls"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if req != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", g.ipc.Token)
		resp, err := g.auditHTTP.Do(req)
		if err != nil {
			g.logger.Warn("MCP audit delivery failed", "error", err)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			g.logger.Warn("MCP audit delivery rejected", "status", resp.StatusCode)
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
