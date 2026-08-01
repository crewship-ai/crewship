package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// newIndexedMemoryMCPTestServer is newMemoryMCPTestServer plus the FTS5
// engines the real sidecar builds at boot — the difference between a
// dispatcher that ranks and one that greps.
func newIndexedMemoryMCPTestServer(t *testing.T) *Server {
	t.Helper()
	s := newMemoryMCPTestServer(t)
	agentEng, err := memory.New(s.agentMemoryBase, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("agent engine: %v", err)
	}
	t.Cleanup(func() { _ = agentEng.Close() })
	crewEng, err := memory.New(s.crewMemoryBase, memory.DefaultConfig())
	if err != nil {
		t.Fatalf("crew engine: %v", err)
	}
	t.Cleanup(func() { _ = crewEng.Close() })
	s.memoryEngine = agentEng
	s.crewMemoryEngine = crewEng
	return s
}

func mcpToolsCall(t *testing.T, s *Server, body string) string {
	t.Helper()
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
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("json-rpc error: %s", resp.Error.Message)
	}
	if resp.Result.IsError {
		t.Fatalf("tool returned isError: %s", resp.Result.Content[0].Text)
	}
	if len(resp.Result.Content) == 0 {
		t.Fatalf("no content blocks; body=%s", w.Body.String())
	}
	return resp.Result.Content[0].Text
}

// TestMemoryMCP_ToolsCall_SearchRanksThroughFTSIndex is the sidecar half
// of #1651: the dispatcher's ranked search only reaches the model if the
// MCP handler hands it the engines the sidecar already keeps. The query
// terms appear in the file in the opposite order and on two lines, so a
// substring scan cannot answer it — a hit proves the engine arrived.
func TestMemoryMCP_ToolsCall_SearchRanksThroughFTSIndex(t *testing.T) {
	s := newIndexedMemoryMCPTestServer(t)
	if err := os.WriteFile(filepath.Join(s.agentMemoryBase, "AGENT.md"),
		[]byte("# notes\nthe rollback happened on tuesday\nharbour deploy was reverted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.memoryEngine.Reindex(); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	out := mcpToolsCall(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call",
		"params":{"name":"memory.search","arguments":{"q":"deploy rollback"}}}`)

	var env struct {
		Hits []struct {
			Source string `json:"source"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode search envelope: %v; out=%s", err, out)
	}
	if len(env.Hits) == 0 || env.Hits[0].Source != "AGENT.md" {
		t.Fatalf("expected an indexed AGENT.md hit through the MCP surface, got %+v (out=%s)", env.Hits, out)
	}
}

// TestMemoryMCP_ToolsCall_WriteThenSearchIsVisible covers the freshness
// half through the wire. Nothing else reindexes an MCP-written file, so
// without the dispatcher's own post-write reindex an agent's notes stay
// invisible to search until the container restarts.
func TestMemoryMCP_ToolsCall_WriteThenSearchIsVisible(t *testing.T) {
	s := newIndexedMemoryMCPTestServer(t)

	mcpToolsCall(t, s, `{"jsonrpc":"2.0","id":8,"method":"tools/call",
		"params":{"name":"memory.write","arguments":{"tier":"AGENT",
		"content":"the rollback happened on tuesday\nharbour deploy was reverted\n","mode":"replace"}}}`)

	out := mcpToolsCall(t, s, `{"jsonrpc":"2.0","id":9,"method":"tools/call",
		"params":{"name":"memory.search","arguments":{"q":"deploy rollback"}}}`)

	var env struct {
		Hits []struct {
			Source string `json:"source"`
		} `json:"hits"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode search envelope: %v; out=%s", err, out)
	}
	if len(env.Hits) == 0 {
		t.Fatalf("a file written through this same MCP surface is not searchable: out=%s", out)
	}
}

// TestMemoryMCP_ToolsList_MatchesAdvertisedCatalogue pins tools/list to
// memory.AdvertisedTools(), the single source of truth the wake prompt
// is also checked against (internal/orchestrator). Before #1651 this
// list was a literal slice here and the prompt named a fifth tool that
// was not in it, so the model was told to call something tools/list
// never showed it.
func TestMemoryMCP_ToolsList_MatchesAdvertisedCatalogue(t *testing.T) {
	s := newMemoryMCPTestServer(t)
	req := httptest.NewRequest("POST", "/mcp/memory",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleMemoryMCP(w, req)

	var resp struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := memory.AdvertisedTools()
	if len(resp.Result.Tools) != len(want) {
		t.Fatalf("tools/list = %d entries, AdvertisedTools() = %d: %+v vs %v",
			len(resp.Result.Tools), len(want), resp.Result.Tools, want)
	}
	for i, tool := range resp.Result.Tools {
		if tool.Name != want[i] {
			t.Errorf("tools/list[%d] = %q, want %q (order is part of the contract)", i, tool.Name, want[i])
		}
		if tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Errorf("tools/list[%d] (%s) is missing its description or schema", i, tool.Name)
		}
	}
}
