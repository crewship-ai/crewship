package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCovNewMCPGatewayUnixSocketBaseURL(t *testing.T) {
	g := NewMCPGateway(nil, &IPCConfig{BaseURL: "/tmp/crewshipd.sock", Token: "t"}, newTestLogger())
	defer g.Close()

	if g.auditBaseURL != "http://localhost" {
		t.Errorf("auditBaseURL = %q, want http://localhost", g.auditBaseURL)
	}
	if g.auditHTTP.Transport == nil {
		t.Error("unix-socket audit client should have a custom dialer transport")
	}
}

func TestCovNewMCPGatewaySkipsBadServers(t *testing.T) {
	servers := []MCPServerInput{
		{ID: "1", Name: "no-endpoint", Transport: "streamable-http"},        // skipped: no endpoint
		{ID: "2", Name: "stdio-one", Transport: "stdio", Command: "echo"},   // skipped: unsupported transport
		{ID: "3", Name: "good", Transport: "sse", Endpoint: "http://x/mcp"}, // kept, default scope
		{ID: "4", Name: "scoped", Scope: "crew", Transport: "streamable-http", Endpoint: "http://y/mcp"},
	}
	g := NewMCPGateway(servers, nil, newTestLogger())
	defer g.Close()

	if len(g.clients) != 2 {
		t.Fatalf("expected 2 clients, got %d", len(g.clients))
	}
	if g.clients["good"].serverScope != "workspace" {
		t.Errorf("default scope = %q, want workspace", g.clients["good"].serverScope)
	}
	if g.clients["scoped"].serverScope != "crew" {
		t.Errorf("scope = %q, want crew", g.clients["scoped"].serverScope)
	}
	if g.auditBaseURL != "" {
		t.Errorf("auditBaseURL should be empty without IPC, got %q", g.auditBaseURL)
	}
}

func TestCovSendAuditEntryDeliversPayload(t *testing.T) {
	type recorded struct {
		path    string
		token   string
		payload map[string]interface{}
	}
	recCh := make(chan recorded, 1)
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		json.NewDecoder(r.Body).Decode(&p)
		recCh <- recorded{path: r.URL.Path, token: r.Header.Get("X-Internal-Token"), payload: p}
		w.WriteHeader(http.StatusCreated)
	}))
	defer audit.Close()

	g := NewMCPGateway(nil, &IPCConfig{
		BaseURL: audit.URL, Token: "audit-tok",
		WorkspaceID: "ws-1", AgentID: "agent-1", CrewID: "crew-1",
	}, newTestLogger())
	defer g.Close()

	g.sendAuditEntry(auditEntry{
		serverID: "srv-9", serverScope: "crew", serverName: "github",
		toolName: "create_issue", durationMS: 42, status: "success",
	})

	select {
	case rec := <-recCh:
		if rec.path != "/api/v1/internal/mcp-tool-calls" {
			t.Errorf("path = %q", rec.path)
		}
		if rec.token != "audit-tok" {
			t.Errorf("token = %q", rec.token)
		}
		if rec.payload["mcp_server_id"] != "srv-9" || rec.payload["mcp_server_scope"] != "crew" ||
			rec.payload["tool_name"] != "create_issue" || rec.payload["status"] != "success" {
			t.Errorf("payload = %+v", rec.payload)
		}
		if rec.payload["workspace_id"] != "ws-1" || rec.payload["agent_id"] != "agent-1" || rec.payload["crew_id"] != "crew-1" {
			t.Errorf("identity fields = %+v", rec.payload)
		}
		if rec.payload["duration_ms"] != float64(42) {
			t.Errorf("duration_ms = %v", rec.payload["duration_ms"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("audit entry never delivered")
	}
}

func TestCovSendAuditEntryToleratesRejectionAndDownServer(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	g := NewMCPGateway(nil, &IPCConfig{BaseURL: rejecting.URL, Token: "t"}, newTestLogger())
	g.sendAuditEntry(auditEntry{serverName: "s", toolName: "t", status: "success"}) // must not panic
	g.Close()
	rejecting.Close()

	// Down server → delivery error path. Also must not panic.
	g2 := NewMCPGateway(nil, &IPCConfig{BaseURL: rejecting.URL, Token: "t"}, newTestLogger())
	g2.sendAuditEntry(auditEntry{serverName: "s", toolName: "t", status: "error", errMsg: "boom"})
	g2.Close()

	// No IPC at all → early return.
	g3 := NewMCPGateway(nil, nil, newTestLogger())
	g3.sendAuditEntry(auditEntry{serverName: "s"})
	g3.Close()
}

// TestCovCallToolEnqueuesAuditEntries verifies the audit plumbing around
// CallTool: a successful call and a tool-error call both produce audit
// entries that the worker delivers to crewshipd.
func TestCovCallToolEnqueuesAuditEntries(t *testing.T) {
	auditCh := make(chan map[string]interface{}, 4)
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/mcp-tool-calls" {
			http.NotFound(w, r)
			return
		}
		var p map[string]interface{}
		json.NewDecoder(r.Body).Decode(&p)
		auditCh <- p
		w.WriteHeader(http.StatusCreated)
	}))
	defer audit.Close()

	mcp := mockMCPServer(t, []mcpToolDef{{Name: "echo", Description: "echoes"}})
	defer mcp.Close()

	g := NewMCPGateway([]MCPServerInput{{
		ID: "srv-1", Name: "mock", DisplayName: "Mock",
		Transport: "streamable-http", Endpoint: mcp.URL,
	}}, &IPCConfig{
		BaseURL: audit.URL, Token: "t", WorkspaceID: "ws-1", AgentID: "a-1",
	}, newTestLogger())
	defer g.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp, err := g.CallTool(ctx, "mock", "echo", json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if resp.IsError {
		t.Fatalf("unexpected tool error: %s", resp.Error)
	}

	select {
	case p := <-auditCh:
		if p["tool_name"] != "echo" || p["status"] != "success" {
			t.Errorf("audit payload = %+v", p)
		}
		if p["mcp_server_id"] != "srv-1" {
			t.Errorf("mcp_server_id = %v", p["mcp_server_id"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("success audit entry never delivered")
	}
}

func TestCovHandleMCPCallToolErrorPaths(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0", Logger: covLogger(),
		MCPServers: []MCPServerInput{{
			ID: "srv-1", Name: "configured", Transport: "streamable-http", Endpoint: "http://127.0.0.1:1/mcp",
		}},
	})

	// Invalid JSON body → 400.
	req := httptest.NewRequest("POST", "http://localhost:9119/mcp/call", strings.NewReader("{broken"))
	w := httptest.NewRecorder()
	srv.handleMCPCallTool(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid JSON: expected 400, got %d", w.Code)
	}

	// Missing tool field → 400.
	req = httptest.NewRequest("POST", "http://localhost:9119/mcp/call", strings.NewReader(`{"server":"configured"}`))
	w = httptest.NewRecorder()
	srv.handleMCPCallTool(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing tool: expected 400, got %d", w.Code)
	}

	// Unknown server → CallTool error → 502.
	req = httptest.NewRequest("POST", "http://localhost:9119/mcp/call", strings.NewReader(`{"server":"ghost","tool":"x"}`))
	w = httptest.NewRecorder()
	srv.handleMCPCallTool(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("unknown server: expected 502, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCovCallToolNotConnected(t *testing.T) {
	g := NewMCPGateway([]MCPServerInput{{
		ID: "srv-1", Name: "lonely", Transport: "streamable-http", Endpoint: "http://127.0.0.1:1/mcp",
	}}, nil, newTestLogger())
	defer g.Close()

	// Never connected (no initialize) → "not connected" error.
	_, err := g.CallTool(context.Background(), "lonely", "any", nil)
	if err == nil {
		t.Fatal("expected error for unconnected server")
	}
	if got := err.Error(); got != `MCP server "lonely" not connected` {
		t.Errorf("error = %q", got)
	}
}

// TestCovCallToolErrorResultAudited verifies the error-status audit branch:
// when the MCP server reports a JSON-RPC error for the call, CallTool wraps
// it as an in-band MCPCallResponse and audits status=error.
func TestCovCallToolErrorResultAudited(t *testing.T) {
	auditCh := make(chan map[string]interface{}, 2)
	audit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]interface{}
		json.NewDecoder(r.Body).Decode(&p)
		auditCh <- p
		w.WriteHeader(http.StatusCreated)
	}))
	defer audit.Close()

	// MCP server that initializes fine but fails every tools/call.
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpcReq jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		w.Header().Set("Content-Type", "application/json")
		switch rpcReq.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-err")
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: rpcReq.ID,
				Result: mustMarshal(initializeResult{ProtocolVersion: "2025-03-26", ServerInfo: &mcpInfo{Name: "err-server"}}),
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0", ID: rpcReq.ID,
				Error: &jsonRPCError{Code: -32000, Message: "tool exploded"},
			})
		}
	}))
	defer mcp.Close()

	g := NewMCPGateway([]MCPServerInput{{
		ID: "srv-err", Name: "flaky", Transport: "streamable-http", Endpoint: mcp.URL,
	}}, &IPCConfig{BaseURL: audit.URL, Token: "t"}, newTestLogger())
	defer g.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp, err := g.CallTool(ctx, "flaky", "boom", nil)
	if err != nil {
		t.Fatalf("CallTool should wrap tool errors in-band, got transport error: %v", err)
	}
	if !resp.IsError || resp.Error == "" {
		t.Errorf("expected in-band error response, got %+v", resp)
	}

	select {
	case p := <-auditCh:
		if p["status"] != "error" {
			t.Errorf("audit status = %v, want error", p["status"])
		}
		if p["error_message"] == "" {
			t.Error("audit error_message should be set")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("error audit entry never delivered")
	}
}
