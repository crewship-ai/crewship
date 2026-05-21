package sidecar

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/memory"
)

// MemoryMCPProtocolVersion is the MCP version this server speaks. Pinned to
// 2024-11-05 — the same release line every supported CLI (Claude Code 2.x,
// Codex Rust 0.128, Gemini 0.40, OpenCode 1.14, Droid current) was built
// against. Bump only when all five CLIs upgrade in lockstep; otherwise an
// adapter handshake will silently fall back to legacy modes.
const MemoryMCPProtocolVersion = "2024-11-05"

// MemoryMCPServerName is the server identity the in-container CLI sees in
// its tool list. Kept short + branded so it's obvious in tool-call traces
// which MCP server the memory.* tools came from.
const MemoryMCPServerName = "crewship-memory"

// memoryMCPRequest is the JSON-RPC 2.0 envelope every MCP method shares.
// The Params shape varies per method; we redecode it per-handler.
type memoryMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // null | number | string per JSON-RPC 2.0
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// memoryMCPResponse mirrors the JSON-RPC 2.0 response envelope. Either
// Result OR Error is populated, never both.
type memoryMCPResponse struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      json.RawMessage    `json:"id,omitempty"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *memoryMCPRPCError `json:"error,omitempty"`
}

type memoryMCPRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// memoryMCPToolDescriptor is one entry in tools/list. Field name MUST be
// "inputSchema" (camelCase) per MCP spec — JSON Schema Draft 2020-12 blob,
// passed through verbatim from memory.ToolSchemas() so the dispatcher and
// MCP wire format stay in lockstep.
type memoryMCPToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// memoryMCPToolCallContent is the per-block content array tools/call
// returns. We always emit a single "text" block — memory tool results are
// plain strings, no binary / image data.
type memoryMCPToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type memoryMCPToolCallResult struct {
	Content []memoryMCPToolCallContent `json:"content"`
	IsError bool                       `json:"isError,omitempty"`
}

// handleMemoryMCP is the JSON-RPC 2.0 entry point CLIs hit at /mcp/memory.
// Three methods are routed:
//
//   - initialize  → handshake; returns protocolVersion + serverInfo
//   - tools/list  → returns memory.ToolSchemas() as MCP tool descriptors
//   - tools/call  → unmarshals name+arguments, dispatches via
//     memory.NewDispatcher with the agent context derived from
//     the sidecar's IPC config + memory base paths
//
// Unknown methods return a JSON-RPC -32601 (method not found). The handler
// is locked to localhost via the buildHandler isLocalhost gate one level
// up — defence in depth means a future bug exposing this on a non-loopback
// listener still needs to bypass two checks.
func (s *Server) handleMemoryMCP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap — MCP requests are tiny
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			Error:   &memoryMCPRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	var req memoryMCPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			Error:   &memoryMCPRPCError{Code: -32700, Message: "invalid JSON: " + err.Error()},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &memoryMCPRPCError{Code: -32600, Message: "jsonrpc must be \"2.0\""},
		})
		return
	}

	switch req.Method {
	case "initialize":
		s.respondMemoryMCPInitialize(w, req)
	case "tools/list":
		s.respondMemoryMCPToolsList(w, req)
	case "tools/call":
		s.respondMemoryMCPToolsCall(w, r, req)
	case "notifications/initialized", "notifications/cancelled":
		// Spec-compliant notification: no response body, ack with 200.
		// Clients that send these MUST NOT wait for a JSON-RPC response.
		w.WriteHeader(http.StatusOK)
	default:
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &memoryMCPRPCError{
				Code:    -32601,
				Message: "method not found: " + req.Method,
			},
		})
	}
}

func (s *Server) respondMemoryMCPInitialize(w http.ResponseWriter, req memoryMCPRequest) {
	result, _ := json.Marshal(map[string]any{
		"protocolVersion": MemoryMCPProtocolVersion,
		// Only "tools" capability — no resources, prompts, or sampling
		// surface. Keeping the capability set narrow means a client that
		// auto-explores does not waste round-trips on methods we do not
		// implement.
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    MemoryMCPServerName,
			"version": "1.0.0",
		},
	})
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) respondMemoryMCPToolsList(w http.ResponseWriter, req memoryMCPRequest) {
	schemas := memory.ToolSchemas()
	tools := make([]memoryMCPToolDescriptor, 0, len(schemas))
	// Stable order so adapters that snapshot the catalog see a deterministic
	// payload — map iteration order is unspecified in Go, and a CLI that
	// caches the tool list across runs would re-fetch on every change.
	for _, name := range []string{"memory.read", "memory.write", "memory.search", "memory.append_daily"} {
		s, ok := schemas[name]
		if !ok {
			continue
		}
		tools = append(tools, memoryMCPToolDescriptor{
			Name:        s.Name,
			Description: s.Description,
			InputSchema: s.InputSchema,
		})
	}
	result, _ := json.Marshal(map[string]any{"tools": tools})
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) respondMemoryMCPToolsCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &memoryMCPRPCError{
				Code: -32602, Message: "invalid params: " + err.Error(),
			},
		})
		return
	}

	dispatcher := memory.NewDispatcher(s.memoryAgentContext())
	toolRes, err := dispatcher.Dispatch(r.Context(), memory.ToolCall{
		Name: params.Name,
		Args: params.Arguments,
	})
	if err != nil {
		// Genuine fatal — return as JSON-RPC error so the CLI knows the
		// dispatcher itself failed (not a recoverable tool error).
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &memoryMCPRPCError{
				Code: -32000, Message: "tool execution failed: " + err.Error(),
			},
		})
		return
	}

	// Dispatcher's IsError-on-ToolResult maps to MCP tools/call result.isError.
	// MCP clients (Claude/Codex/Gemini/OpenCode/Droid) surface isError back
	// to the model as a recoverable signal — same semantics as Anthropic's
	// tool_result is_error contract.
	out := memoryMCPToolCallResult{
		Content: []memoryMCPToolCallContent{{Type: "text", Text: toolRes.Content}},
		IsError: toolRes.IsError,
	}
	result, _ := json.Marshal(out)
	writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

// memoryAgentContext builds the per-call routing data the dispatcher needs.
// Derived from the sidecar's persisted IPC config + the memory base paths
// resolved at startup — the same fields handleMemoryWrite already uses, so
// the MCP path and the legacy HTTP path land in identical AgentContexts and
// can never diverge in their tier resolution.
func (s *Server) memoryAgentContext() memory.AgentContext {
	ac := memory.AgentContext{
		AgentMemoryDir: s.agentMemoryBase,
		CrewMemoryDir:  s.crewMemoryBase,
	}
	if s.ipc != nil {
		ac.AgentID = s.ipc.AgentID
		ac.CrewID = s.ipc.CrewID
		ac.WorkspaceID = s.ipc.WorkspaceID
	}
	return ac
}
