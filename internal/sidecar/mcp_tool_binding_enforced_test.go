package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCallToolRefusesDisabledToolBindings pins #2178: a tool switched off in
// mcp_tool_bindings (delivered as MCPServerInput.DisabledTools) is refused by
// the gateway before any request reaches the MCP server, and is left out of
// the advertised catalog. Other tools on the same server keep working.
func TestCallToolRefusesDisabledToolBindings(t *testing.T) {
	inner := mockMCPServer(t, []mcpToolDef{{Name: "read_issue"}, {Name: "delete_repo"}})
	defer inner.Close()
	var upstreamCalls atomic.Int32
	var lastCalled atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			var req struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(raw, &req)
			if req.Method == "tools/call" {
				upstreamCalls.Add(1)
				var p toolCallParams
				_ = json.Unmarshal(req.Params, &p)
				lastCalled.Store(p.Name)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		inner.Config.Handler.ServeHTTP(w, r)
	}))
	defer srv.Close()

	gw := NewMCPGateway([]MCPServerInput{{
		ID: "srv1", Name: "github", DisplayName: "GitHub", Transport: "streamable-http", Endpoint: srv.URL,
		DisabledTools: []string{"delete_repo"},
	}}, nil, newTestLogger())
	defer gw.Close()
	ctx := context.Background()
	if err := gw.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.DiscoverTools(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := gw.CallTool(ctx, "github", "delete_repo", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled tool: err = %v, want a disabled refusal", err)
	}
	if n := upstreamCalls.Load(); n != 0 {
		t.Fatalf("disabled tool reached the MCP server %d time(s)", n)
	}

	result, err := gw.CallTool(ctx, "github", "read_issue", json.RawMessage(`{}`))
	if err != nil || result.IsError {
		t.Fatalf("enabled tool: %v %+v", err, result)
	}
	if n, name := upstreamCalls.Load(), lastCalled.Load(); n != 1 || name != "read_issue" {
		t.Fatalf("enabled tool upstream calls = %d (%v), want 1 read_issue", n, name)
	}

	for _, tool := range gw.ListTools() {
		if tool.Name == "delete_repo" {
			t.Fatal("disabled tool is advertised by /mcp/tools")
		}
	}
	if got := len(gw.ListTools()); got != 1 {
		t.Fatalf("catalog has %d tools, want only read_issue", got)
	}
}
