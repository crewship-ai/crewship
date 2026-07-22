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

// refuseUnauthorizedMemory is the memory surface's chokepoint. buildHandler
// calls it for EVERY request whose path satisfies isMemoryRoutePath, before the
// route switch picks a handler, so no memory handler has to remember to opt in.
//
// It returns true when the request must not reach a handler — the refusal has
// already been written and the caller must return immediately. Two distinct
// cases, both 403:
//
//  1. Token-less downgrade on a crew with per-agent tokens (a sibling dropping
//     its Authorization header to fall back to ambient boot identity).
//
//  2. A token matching no crew member — a forgery. CRE-153 gated only case 1,
//     on the reasoning (identity.go) that "a request with a token is resolved
//     through actingIdentity, which refuses forgeries on its own". That is true
//     of /mcp/memory/<slug>, and false of the five legacy /memory/* routes,
//     which call actingIdentity nowhere. So `Authorization: Bearer anything`
//     walked straight past the gate and read the boot agent's private tier —
//     a cheaper bypass than the one being closed. The check belongs here, at
//     the chokepoint, not in each handler: that is the mistake this whole guard
//     exists to stop repeating.
//
// A THIRD case used to live here: on the legacy routes, an authenticated crew
// member that is not the boot agent was refused outright for AGENT-scoped
// requests, because those five handlers resolved the agent tier from
// s.agentMemoryBase — the boot agent's memory — with no identity resolution
// at all. #1301 replaced that refusal with the real fix: the legacy handlers
// now resolve the ACTING agent's own tier via legacyMemoryEffectiveSlug +
// peerMemoryEngineFor/memoryAgentContextFor, so a sibling with a valid token
// reaches its own AGENT.md instead of either alpha's or a 403. Scope=crew was
// never affected by that case (crewMemoryBase is ONE directory shared by the
// whole crew by construction), and still isn't — it doesn't need this guard's
// help, since there's no per-agent crew tier to cross.
//
// The MCP transport previously answered its own refusal with HTTP 200 and a
// JSON-RPC error, which made every downgrade attempt indistinguishable from a
// success in access logs; the JSON-RPC error envelope is preserved for protocol
// clients but the HTTP status now tells the truth.
func (s *Server) refuseUnauthorizedMemory(w http.ResponseWriter, r *http.Request) bool {
	if s.tokenlessDowngrade(r) {
		return s.writeMemoryRefusal(w, r, "per-agent token required",
			"memory route: refusing token-less request on a crew with per-agent tokens")
	}

	_, actorSlug, present, ok := s.actingIdentity(r)
	if present && !ok {
		return s.writeMemoryRefusal(w, r, "unrecognized agent token",
			"memory route: refusing request with a token matching no crew member")
	}

	// A token matching a roster entry whose Slug is EMPTY (#1341 follow-up).
	// "" means two different things to the tier resolvers and only one of them
	// is safe: memoryAgentContextFor("") and peerMemoryEngineFor("") both read
	// it as "the sidecar's own boot agent", so a slugless member would be
	// silently promoted to the boot agent's private tier. The MCP handler
	// refuses this inline (handleMemoryMCPForAgent), but the five legacy
	// /memory/* routes resolved it through legacyMemoryEffectiveSlug straight
	// into that promotion. Refuse at the chokepoint so both transports agree.
	// Unreachable today (agents.slug is NOT NULL and every create path
	// validates it) — this is the fence for the day that stops being true.
	if present && ok && actorSlug == "" {
		return s.writeMemoryRefusal(w, r, "agent identity has no slug",
			"memory route: refusing a token that resolves to an empty agent slug")
	}

	return false
}

// writeMemoryRefusal logs the refusal and writes it in the shape the route
// expects — a JSON-RPC error envelope for the MCP transport, plain JSON for the
// legacy routes — then reports true so the caller returns immediately.
func (s *Server) writeMemoryRefusal(w http.ResponseWriter, r *http.Request, clientMsg, logMsg string) bool {
	if s.logger != nil {
		s.logger.Warn(logMsg, "method", r.Method, "path", r.URL.Path)
	}
	if isMemoryMCPPath(r.URL.Path) {
		writeJSONResponse(w, http.StatusForbidden, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpRequestID(r),
			Error:   &memoryMCPRPCError{Code: -32001, Message: clientMsg},
		})
		return true
	}
	writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": clientMsg})
	return true
}

// isMemoryMCPPath reports whether p is served by the JSON-RPC memory MCP
// transport, which needs its refusal shaped as a JSON-RPC error envelope rather
// than the plain {"error": ...} the legacy routes return.
func isMemoryMCPPath(p string) bool {
	return p == "/mcp/memory" || strings.HasPrefix(p, "/mcp/memory/")
}
