package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// mockMCPServer creates a test HTTP server that speaks MCP streamable-http protocol.
func mockMCPServer(t *testing.T, tools []mcpToolDef) *httptest.Server {
	t.Helper()
	var sessionID string
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch rpcReq.Method {
		case "initialize":
			sessionID = "test-session-123"
			w.Header().Set("Mcp-Session-Id", sessionID)
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result: mustMarshal(initializeResult{
					ProtocolVersion: "2025-03-26",
					ServerInfo:      &mcpInfo{Name: "test-server", Version: "1.0"},
				}),
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  mustMarshal(toolsListResult{Tools: tools}),
			})
		case "tools/call":
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result: mustMarshal(toolCallResult{
					Content: []toolContent{{Type: "text", Text: "tool result OK"}},
				}),
			})
		default:
			json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Error:   &jsonRPCError{Code: -32601, Message: "Method not found"},
			})
		}
	}))
}

func mustMarshal(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestMCPGateway_ConnectAndDiscoverTools(t *testing.T) {
	tools := []mcpToolDef{
		{Name: "gmail_send", Description: "Send email"},
		{Name: "gmail_search", Description: "Search emails"},
	}
	srv := mockMCPServer(t, tools)
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "gmail", DisplayName: "Gmail", Transport: "streamable-http", Endpoint: srv.URL},
	}, nil, newTestLogger())

	ctx := context.Background()
	if err := gw.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	discovered, err := gw.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools failed: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(discovered))
	}
	if discovered[0].ServerName != "gmail" || discovered[0].Name != "gmail_send" {
		t.Errorf("unexpected tool: %+v", discovered[0])
	}
}

func TestMCPGateway_CallTool(t *testing.T) {
	srv := mockMCPServer(t, []mcpToolDef{{Name: "test_tool", Description: "Test"}})
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "testserver", DisplayName: "Test", Transport: "streamable-http", Endpoint: srv.URL},
	}, nil, newTestLogger())

	ctx := context.Background()
	gw.Connect(ctx)
	gw.DiscoverTools(ctx)

	result, err := gw.CallTool(ctx, "testserver", "test_tool", json.RawMessage(`{"key":"value"}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(string(result.Content), "tool result OK") {
		t.Errorf("unexpected content: %s", string(result.Content))
	}
}

func TestMCPGateway_CallTool_UnknownServer(t *testing.T) {
	gw := NewMCPGateway(nil, nil, newTestLogger())

	_, err := gw.CallTool(context.Background(), "nonexistent", "tool", nil)
	if err == nil {
		t.Error("expected error for unknown server")
	}
}

func TestMCPGateway_CredentialInjection(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess")
		// Respond to initialize
		var rpcReq jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: rpcReq.ID,
			Result: mustMarshal(initializeResult{ProtocolVersion: "2025-03-26"}),
		})
	}))
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{
			ID: "srv1", Name: "secure", Transport: "streamable-http", Endpoint: srv.URL,
			Credential: &MCPCredInput{Token: "secret-token-123", Type: "bearer"},
		},
	}, nil, newTestLogger())

	gw.Connect(context.Background())

	if capturedAuth != "Bearer secret-token-123" {
		t.Errorf("expected 'Bearer secret-token-123', got %q", capturedAuth)
	}
}

func TestMCPGateway_APIKeyCredential(t *testing.T) {
	var capturedHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-Custom-Key")
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess")
		var rpcReq jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: rpcReq.ID,
			Result: mustMarshal(initializeResult{ProtocolVersion: "2025-03-26"}),
		})
	}))
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{
			ID: "srv1", Name: "apikey", Transport: "streamable-http", Endpoint: srv.URL,
			Credential: &MCPCredInput{Token: "my-api-key", Type: "api_key", Header: "X-Custom-Key"},
		},
	}, nil, newTestLogger())

	gw.Connect(context.Background())

	if capturedHeader != "my-api-key" {
		t.Errorf("expected 'my-api-key', got %q", capturedHeader)
	}
}

func TestMCPGateway_Status(t *testing.T) {
	srv := mockMCPServer(t, nil)
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "server1", DisplayName: "Server One", Transport: "streamable-http", Endpoint: srv.URL},
	}, nil, newTestLogger())

	gw.Connect(context.Background())

	statuses := gw.Status()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if !statuses[0].Connected {
		t.Error("expected server to be connected")
	}
	if statuses[0].Name != "server1" {
		t.Errorf("expected 'server1', got %q", statuses[0].Name)
	}
}

func TestMCPGateway_StdioSkipped(t *testing.T) {
	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "local", Transport: "stdio", Command: "echo"},
	}, nil, newTestLogger())

	// stdio is not yet supported, should be skipped
	if len(gw.clients) != 0 {
		t.Errorf("expected 0 clients for stdio, got %d", len(gw.clients))
	}
}

func TestMCPGateway_ListToolsCached(t *testing.T) {
	gw := NewMCPGateway(nil, nil, newTestLogger())

	// No servers = empty tools
	tools := gw.ListTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(tools))
	}
}

func TestMCPGateway_SessionTermination(t *testing.T) {
	var deleteReceived bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteReceived = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "to-be-terminated")
		var rpcReq jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		json.NewEncoder(w).Encode(jsonRPCResponse{
			JSONRPC: "2.0", ID: rpcReq.ID,
			Result: mustMarshal(initializeResult{ProtocolVersion: "2025-03-26"}),
		})
	}))
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "srv1", Name: "test", Transport: "streamable-http", Endpoint: srv.URL},
	}, nil, newTestLogger())

	gw.Connect(context.Background())
	gw.Close()

	if !deleteReceived {
		t.Error("expected DELETE request for session termination")
	}
}

// Test sidecar handler integration
func TestSidecarMCPHandlers_NoGateway(t *testing.T) {
	srv := NewServer(ServerConfig{Logger: newTestLogger()})

	// /mcp/tools should return empty array when no gateway
	req := httptest.NewRequest("GET", "/mcp/tools", nil)
	rr := httptest.NewRecorder()
	srv.handleMCPListTools(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var tools []MCPTool
	json.NewDecoder(rr.Body).Decode(&tools)
	if len(tools) != 0 {
		t.Errorf("expected empty tools, got %d", len(tools))
	}

	// /mcp/status should show disabled
	req = httptest.NewRequest("GET", "/mcp/status", nil)
	rr = httptest.NewRecorder()
	srv.handleMCPStatus(rr, req)
	var status map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&status)
	if status["enabled"].(bool) {
		t.Error("expected enabled=false")
	}

	// /mcp/call should return 503
	req = httptest.NewRequest("POST", "/mcp/call", strings.NewReader(`{"server":"x","tool":"y"}`))
	rr = httptest.NewRecorder()
	srv.handleMCPCallTool(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestSidecarMCPHandlers_WithGateway(t *testing.T) {
	tools := []mcpToolDef{
		{Name: "send_email", Description: "Send an email"},
	}
	mcpSrv := mockMCPServer(t, tools)
	defer mcpSrv.Close()

	srv := NewServer(ServerConfig{
		MCPServers: []MCPServerInput{
			{ID: "srv1", Name: "email", DisplayName: "Email", Transport: "streamable-http", Endpoint: mcpSrv.URL},
		},
		Logger: newTestLogger(),
	})

	// Connect and discover
	srv.mcpGateway.Connect(context.Background())
	srv.mcpGateway.DiscoverTools(context.Background())

	// /mcp/tools
	req := httptest.NewRequest("GET", "/mcp/tools", nil)
	rr := httptest.NewRecorder()
	srv.handleMCPListTools(rr, req)
	var toolList []MCPTool
	json.NewDecoder(rr.Body).Decode(&toolList)
	if len(toolList) != 1 || toolList[0].Name != "send_email" {
		t.Errorf("unexpected tools: %+v", toolList)
	}

	// /mcp/call
	callBody := `{"server":"email","tool":"send_email","input":{"to":"test@test.com"}}`
	req = httptest.NewRequest("POST", "/mcp/call", strings.NewReader(callBody))
	rr = httptest.NewRecorder()
	srv.handleMCPCallTool(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var callResp MCPCallResponse
	json.NewDecoder(rr.Body).Decode(&callResp)
	if callResp.IsError {
		t.Errorf("unexpected error: %s", callResp.Error)
	}

	// /mcp/status
	req = httptest.NewRequest("GET", "/mcp/status", nil)
	rr = httptest.NewRecorder()
	srv.handleMCPStatus(rr, req)
	var statusResp map[string]interface{}
	json.NewDecoder(rr.Body).Decode(&statusResp)
	if !statusResp["enabled"].(bool) {
		t.Error("expected enabled=true")
	}
	servers := statusResp["servers"].([]interface{})
	if len(servers) != 1 {
		t.Errorf("expected 1 server, got %d", len(servers))
	}
}

func TestMCPCallRequest_Validation(t *testing.T) {
	srv := NewServer(ServerConfig{
		MCPServers: []MCPServerInput{
			{ID: "srv1", Name: "test", Transport: "streamable-http", Endpoint: "http://localhost:1"},
		},
		Logger: newTestLogger(),
	})

	tests := []struct {
		name string
		body string
		code int
	}{
		{"empty body", `{}`, http.StatusBadRequest},
		{"missing tool", `{"server":"test"}`, http.StatusBadRequest},
		{"missing server", `{"tool":"x"}`, http.StatusBadRequest},
		{"invalid json", `{broken`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/mcp/call", strings.NewReader(tt.body))
			rr := httptest.NewRecorder()
			srv.handleMCPCallTool(rr, req)
			if rr.Code != tt.code {
				t.Errorf("expected %d, got %d: %s", tt.code, rr.Code, rr.Body.String())
			}
		})
	}
}

// mockSSEMCPServer creates a test HTTP server that responds with SSE format (deprecated MCP transport).
func mockSSEMCPServer(t *testing.T, tools []mcpToolDef) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var rpcReq jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")

		var rpcResp jsonRPCResponse
		switch rpcReq.Method {
		case "initialize":
			rpcResp = jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result: mustMarshal(initializeResult{
					ProtocolVersion: "2025-03-26",
					ServerInfo:      &mcpInfo{Name: "sse-server", Version: "1.0"},
				}),
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			rpcResp = jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result:  mustMarshal(toolsListResult{Tools: tools}),
			}
		case "tools/call":
			rpcResp = jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Result: mustMarshal(toolCallResult{
					Content: []toolContent{{Type: "text", Text: "sse tool result OK"}},
				}),
			}
		default:
			rpcResp = jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      rpcReq.ID,
				Error:   &jsonRPCError{Code: -32601, Message: "Method not found"},
			}
		}

		data, _ := json.Marshal(rpcResp)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	}))
}

func TestMCPGateway_SSETransport_ConnectAndDiscover(t *testing.T) {
	tools := []mcpToolDef{
		{Name: "sse_tool_1", Description: "SSE tool one"},
		{Name: "sse_tool_2", Description: "SSE tool two"},
	}
	srv := mockSSEMCPServer(t, tools)
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "sse1", Name: "sse-server", DisplayName: "SSE Server", Transport: "sse", Endpoint: srv.URL},
	}, nil, newTestLogger())

	if len(gw.clients) != 1 {
		t.Fatalf("expected 1 client for sse transport, got %d", len(gw.clients))
	}

	ctx := context.Background()
	if err := gw.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	discovered, err := gw.DiscoverTools(ctx)
	if err != nil {
		t.Fatalf("DiscoverTools failed: %v", err)
	}
	if len(discovered) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(discovered))
	}
	if discovered[0].ServerName != "sse-server" || discovered[0].Name != "sse_tool_1" {
		t.Errorf("unexpected tool: %+v", discovered[0])
	}
}

func TestMCPGateway_SSETransport_CallTool(t *testing.T) {
	srv := mockSSEMCPServer(t, []mcpToolDef{{Name: "sse_test", Description: "Test"}})
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "sse1", Name: "sse-srv", DisplayName: "SSE", Transport: "sse", Endpoint: srv.URL},
	}, nil, newTestLogger())

	ctx := context.Background()
	gw.Connect(ctx)
	gw.DiscoverTools(ctx)

	result, err := gw.CallTool(ctx, "sse-srv", "sse_test", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(string(result.Content), "sse tool result OK") {
		t.Errorf("unexpected content: %s", string(result.Content))
	}
}

func TestMCPGateway_SSETransport_NoDeleteOnClose(t *testing.T) {
	var deleteReceived bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteReceived = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var rpcReq jsonRPCRequest
		json.NewDecoder(r.Body).Decode(&rpcReq)
		if rpcReq.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		resp := jsonRPCResponse{
			JSONRPC: "2.0", ID: rpcReq.ID,
			Result: mustMarshal(initializeResult{ProtocolVersion: "2025-03-26"}),
		}
		data, _ := json.Marshal(resp)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	}))
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{
		{ID: "sse1", Name: "sse-test", Transport: "sse", Endpoint: srv.URL},
	}, nil, newTestLogger())

	gw.Connect(context.Background())
	gw.Close()

	if deleteReceived {
		t.Error("SSE transport should not send DELETE for session termination")
	}
}

func TestParseSSEResponse_Basic(t *testing.T) {
	input := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"
	resp, err := parseSSEResponse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseSSEResponse failed: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("expected ID 1, got %d", resp.ID)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
}

func TestParseSSEResponse_MultiLineData(t *testing.T) {
	// Multi-line data field (each line prefixed with "data:")
	input := "event: message\ndata: {\"jsonrpc\":\"2.0\",\ndata: \"id\":1,\"result\":{}}\n\n"
	resp, err := parseSSEResponse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseSSEResponse failed: %v", err)
	}
	if resp.ID != 1 {
		t.Errorf("expected ID 1, got %d", resp.ID)
	}
}

func TestParseSSEResponse_NoEventType(t *testing.T) {
	// No event type defaults to message
	input := "data: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{}}\n\n"
	resp, err := parseSSEResponse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseSSEResponse failed: %v", err)
	}
	if resp.ID != 2 {
		t.Errorf("expected ID 2, got %d", resp.ID)
	}
}

func TestParseSSEResponse_NoMessage(t *testing.T) {
	input := "event: ping\ndata: {}\n\n"
	_, err := parseSSEResponse(strings.NewReader(input))
	if err == nil {
		t.Error("expected error for non-message event")
	}
}

// prevent unused import
var _ = fmt.Sprint
