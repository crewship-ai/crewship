package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// mcpRequestID best-effort extracts the JSON-RPC `id` from an MCP request body
// so a refusal can be correlated with the call that triggered it. It CONSUMES
// r.Body, so it may only be called on a path that is not going to hand the
// request to a real handler (i.e. immediately before writing an error).
//
// JSON-RPC 2.0 restricts `id` to string | number | null. Anything else — an
// object, an array, an unparseable body, an oversized body — degrades to null
// rather than reflecting attacker-shaped JSON back into our own response
// envelope. The read is capped at sidecarMaxBodyBytes for the same reason every
// other sidecar body is.
func mcpRequestID(r *http.Request) json.RawMessage {
	if r == nil || r.Body == nil {
		return mcpNullID
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, sidecarMaxBodyBytes))
	if err != nil {
		return mcpNullID
	}
	var env memoryMCPRequest
	if err := json.Unmarshal(raw, &env); err != nil || len(env.ID) == 0 {
		return mcpNullID
	}
	var decoded any
	if err := json.Unmarshal(env.ID, &decoded); err != nil {
		return mcpNullID
	}
	switch decoded.(type) {
	case string, float64:
		return env.ID
	default:
		return mcpNullID
	}
}

// isMemoryRoutePath reports whether p is part of the memory surface — BOTH the
// MCP transport (/mcp/memory, /mcp/memory/<slug>) and the legacy HTTP routes
// (/memory/read, /memory/write, /memory/search, /memory/status,
// /memory/reindex). Prefix-based on purpose: a memory route added later is
// covered by the guard the moment it is registered under one of these prefixes,
// which is exactly what per-handler opt-in failed to deliver (#1254 item A /
// CRE-153 — the five legacy routes were missed because each handler had to
// remember to call the check itself).
func isMemoryRoutePath(p string) bool {
	return p == "/memory" || strings.HasPrefix(p, "/memory/") || isMemoryMCPPath(p)
}

// refuseTokenlessMemory is the memory surface's chokepoint. buildHandler calls
// it for EVERY request whose path satisfies isMemoryRoutePath, before the route
// switch picks a handler, so no memory handler has to remember to opt in.
//
// It returns true when the request was a token-less downgrade attempt on a crew
// with per-agent tokens provisioned — in which case the refusal has already
// been written and the caller must return immediately.
//
// Status is 403, matching /query and /escalate. The MCP transport previously
// answered its own refusal with HTTP 200 and a JSON-RPC error, which made every
// downgrade attempt indistinguishable from a success in access logs; the
// JSON-RPC error envelope is preserved for protocol clients but the HTTP status
// now tells the truth.
func (s *Server) refuseTokenlessMemory(w http.ResponseWriter, r *http.Request) bool {
	if !s.tokenlessDowngrade(r) {
		return false
	}
	if s.logger != nil {
		s.logger.Warn("memory route: refusing token-less request on a crew with per-agent tokens",
			"method", r.Method, "path", r.URL.Path)
	}
	if isMemoryMCPPath(r.URL.Path) {
		writeJSONResponse(w, http.StatusForbidden, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequestID(r),
			Error:   &memoryMCPRPCError{Code: -32001, Message: "per-agent token required"},
		})
		return true
	}
	writeJSONResponse(w, http.StatusForbidden, map[string]string{
		"error": "per-agent token required",
	})
	return true
}

// isMemoryMCPPath reports whether p is served by the JSON-RPC memory MCP
// transport, which needs its refusal shaped as a JSON-RPC error envelope rather
// than the plain {"error": ...} the legacy routes return.
func isMemoryMCPPath(p string) bool {
	return p == "/mcp/memory" || strings.HasPrefix(p, "/mcp/memory/")
}
