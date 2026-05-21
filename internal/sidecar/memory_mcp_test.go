package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// TestMemoryMCP_ToolsList_ReturnsFourMemoryTools is the contract test for
// the inbound MCP server the sidecar hosts for in-container CLIs. tools/list
// must surface exactly the four memory tools that memory.ToolSchemas()
// publishes — adapters key on this list (Claude's --mcp-config, Codex's
// .codex/config.toml, Gemini's settings.json, OpenCode's opencode.json,
// Droid's .factory/mcp.json) and any drift between dispatcher schemas and
// what the MCP server advertises silently breaks every adapter's wiring.
func TestMemoryMCP_ToolsList_ReturnsFourMemoryTools(t *testing.T) {
	s := newMemoryMCPTestServer(t)

	req := httptest.NewRequest("POST", "/mcp/memory",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()

	s.handleMemoryMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Result.Tools) != 4 {
		t.Fatalf("tools count = %d, want 4 (memory.read/write/search/append_daily). got=%+v",
			len(resp.Result.Tools), resp.Result.Tools)
	}
	// Assert exact deterministic order (not just set membership) — the
	// endpoint contract promises stable order so adapter wiring tests +
	// any model that caches tool indices can rely on it.
	got := make([]string, 0, len(resp.Result.Tools))
	for _, tt := range resp.Result.Tools {
		got = append(got, tt.Name)
	}
	want := []string{"memory.read", "memory.write", "memory.search", "memory.append_daily"}
	if len(got) != len(want) {
		t.Fatalf("tools/list length mismatch: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tools/list order mismatch at index %d: got=%v want=%v", i, got, want)
		}
	}
}

// TestMemoryMCP_ToolsCall_RoutesToDispatcher verifies a tools/call request
// for memory.write actually goes through memory.NewDispatcher.Dispatch and
// writes the file to the agent memory dir the sidecar was configured with.
// This is the wire bridge: the adapters care only that an MCP tool call lands
// in the dispatcher with the right AgentContext; this test pins that.
func TestMemoryMCP_ToolsCall_RoutesToDispatcher(t *testing.T) {
	s := newMemoryMCPTestServer(t)

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"memory.write",
		         "arguments":{"tier":"AGENT","content":"hello from MCP\n","mode":"replace"}}}`
	req := httptest.NewRequest("POST", "/mcp/memory", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()

	s.handleMemoryMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// File must exist on disk — the dispatcher wrote through.
	got, err := os.ReadFile(filepath.Join(s.agentMemoryBase, "AGENT.md"))
	if err != nil {
		t.Fatalf("AGENT.md not written: %v", err)
	}
	if string(got) != "hello from MCP\n" {
		t.Fatalf("AGENT.md = %q, want %q", got, "hello from MCP\n")
	}
}

// TestMemoryMCP_ToolsCall_UnknownTool_ReturnsIsError verifies that an
// unknown tool name comes back as an MCP tool error (isError=true) and NOT
// as a JSON-RPC fatal — that's the dispatcher's recoverable-vs-fatal split
// surfacing through the MCP wire format so the model can correct and retry.
func TestMemoryMCP_ToolsCall_UnknownTool_ReturnsIsError(t *testing.T) {
	s := newMemoryMCPTestServer(t)
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"memory.bogus","arguments":{}}}`
	req := httptest.NewRequest("POST", "/mcp/memory", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()

	s.handleMemoryMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Result.IsError {
		t.Fatalf("expected isError=true, got result=%+v", resp.Result)
	}
}

// TestMemoryMCP_Initialize_ReturnsProtocolVersion exercises the JSON-RPC
// initialize handshake every MCP client (Claude, Codex, Gemini, OpenCode,
// Droid) sends before tools/list. Missing this would make every adapter
// silently drop the connection at startup.
func TestMemoryMCP_Initialize_ReturnsProtocolVersion(t *testing.T) {
	s := newMemoryMCPTestServer(t)
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize",
		"params":{"protocolVersion":"2024-11-05",
		         "capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
	req := httptest.NewRequest("POST", "/mcp/memory", strings.NewReader(body))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()

	s.handleMemoryMCP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.ProtocolVersion == "" {
		t.Errorf("initialize result missing protocolVersion")
	}
	if resp.Result.ServerInfo.Name == "" {
		t.Errorf("initialize result missing serverInfo.name")
	}
}

// newMemoryMCPTestServer builds a sidecar Server scaffold sufficient for
// the memory MCP routes — temp dirs for agent/crew memory, no proxy/
// allowlist/credstore needed since the MCP handler only touches
// agentMemoryBase + crewMemoryBase to construct the AgentContext.
func newMemoryMCPTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	agentBase := filepath.Join(root, "agent", ".memory")
	crewBase := filepath.Join(root, "crew", ".memory")
	if err := os.MkdirAll(agentBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(crewBase, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Server{
		agentMemoryBase: agentBase,
		crewMemoryBase:  crewBase,
		ipc: &IPCConfig{
			AgentID:     "agent-1",
			AgentSlug:   "alpha",
			CrewID:      "crew-1",
			WorkspaceID: "ws-1",
		},
	}
}

// keep the test package's memory import live even when only a subset of the
// helpers in this file consult it directly — avoids a future test addition
// having to re-add the import.
var _ = memory.ToolSchemas
