package sidecar

import (
	"bytes"
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
// already been written and the caller must return immediately. Three distinct
// cases, all 403:
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
//  3. On the LEGACY routes, for AGENT-scoped requests only: an authenticated
//     crew member that is not the boot agent. Those five handlers resolve the
//     agent tier from s.agentMemoryBase — the boot agent's memory — with no
//     identity resolution at all, so a sibling holding a perfectly valid token
//     of its own still read and overwrote ALPHA's private AGENT.md.
//
//     The scope qualifier is load-bearing and was missing from the first cut of
//     this fix, which refused the whole legacy prefix. `scope=crew` resolves to
//     crewMemoryBase — ONE directory shared by every member of the crew by
//     construction (orchestrator sets CrewMemoryPath for every agent with a
//     CrewID, not just leads). There is no per-agent crew tier to cross, so
//     refusing it protected nothing and broke sibling access to shared memory:
//     a functional regression wearing a security fix's clothes.
//
//     Serving each agent its OWN agent tier on these routes is the better end
//     state and is deliberately not attempted here: read/write are a base-path
//     swap, but search/status/reindex run through a single indexed
//     memory.Engine bound to one tier, so per-agent means per-agent engine
//     instances — lifecycle, close, concurrency, per-tier index files. That is
//     a subsystem, not a security patch. Tracked in #1301.
//
// The MCP transport previously answered its own refusal with HTTP 200 and a
// JSON-RPC error, which made every downgrade attempt indistinguishable from a
// success in access logs; the JSON-RPC error envelope is preserved for protocol
// clients but the HTTP status now tells the truth.
func (s *Server) refuseUnauthorizedMemory(w http.ResponseWriter, r *http.Request) bool {
	if s.tokenlessDowngrade(r) {
		return s.writeMemoryRefusal(w, r, "per-agent token required",
			"memory route: refusing token-less request on a crew with per-agent tokens", "")
	}

	_, actorSlug, present, ok := s.actingIdentity(r)
	if present && !ok {
		return s.writeMemoryRefusal(w, r, "unrecognized agent token",
			"memory route: refusing request with a token matching no crew member", "")
	}

	// Legacy routes serve the BOOT agent's agent tier. The crew tier they also
	// serve is shared by construction and stays open to every member.
	//
	// Note the comparison is written to fail CLOSED on an empty boot slug. The
	// first cut had `s.memoryAgentSlug != "" && actorSlug != s.memoryAgentSlug`,
	// which silently disabled this whole check when the slug was empty — a
	// sibling then read the boot tier again.
	//
	// An empty slug should not be reachable: assembleSystemPrompt rejects
	// req.AgentSlug unless it matches `^[a-zA-Z0-9][a-zA-Z0-9_-]*$` (empty
	// fails) and aborts the run, and that check runs before ensureSidecar
	// builds SidecarMemoryConfig from the same field — orchestrator_run.go,
	// pinned by TestRunAgent_EmptySlugRejectedBeforeSidecarMemoryConfig.
	// What keeps the empty case benign even if that ordering ever slips is that
	// the SAME field feeds both halves — AgentSlug and BasePath
	// (/crew/agents/<slug>/.memory) — so the slug the guard compares against
	// and the tier the legacy handlers serve cannot name different agents. An
	// empty slug therefore means the whole memory config is degenerate, not
	// that the guard is looking at the wrong agent. Refusing is still the only
	// defensible answer to "I cannot tell who the boot agent is", and it must
	// not depend on a check enforced in a different package.
	if present && ok && !isMemoryMCPPath(r.URL.Path) &&
		(s.memoryAgentSlug == "" || actorSlug != s.memoryAgentSlug) &&
		memoryRequestScope(r) != memoryScopeCrew {
		return s.writeMemoryRefusal(w, r,
			legacyCrossAgentMessage(r.URL.Path, actorSlug),
			"memory route: refusing cross-agent access to the agent tier on a legacy route",
			actorSlug)
	}

	return false
}

const memoryScopeCrew = "crew"

// legacyCrossAgentMessage tells the refused caller where its own agent tier
// actually is. Route-dependent on purpose: the memory MCP transport exposes
// memory.read / memory.write / memory.search / memory.append_daily and NOTHING
// else, so pointing a /memory/status or /memory/reindex caller at
// /mcp/memory/<slug> would send it somewhere that cannot serve the request —
// a refusal that lies about the alternative is how a guard gets deleted by the
// next person to hit it.
func legacyCrossAgentMessage(path, actorSlug string) string {
	const shared = "; scope=crew is shared and remains available to every crew member"
	switch path {
	case "/memory/read", "/memory/write", "/memory/search":
		return "this route serves the boot agent's memory tier; use /mcp/memory/" +
			actorSlug + " for your own agent tier" + shared
	default:
		// /memory/status, /memory/reindex, and anything added under the prefix.
		return "this route serves the boot agent's memory tier and has no " +
			"per-agent equivalent yet (see issue #1301)" + shared
	}
}

// memoryRequestScope reports which tier the request targets, applying the same
// default every legacy handler applies ("" → "agent").
//
// The guard runs in front of the route switch, so it must read the scope from
// the SAME PLACE the handler will — and that is a per-route fact, not a
// per-method one. The obvious rule (GET → query, POST → body) is wrong:
// /memory/reindex is registered POST and reads its scope from the QUERY
// (memory.go, handleMemoryReindex). Under the method rule the guard read an
// empty body, resolved "agent", and refused a legitimate
// `POST /memory/reindex?scope=crew` from a crew member — while
// `POST /memory/reindex` with body {"scope":"crew"} made the guard see "crew"
// and let it through to a handler that saw no query, defaulted to "agent", and
// reindexed the BOOT agent's tier for a sibling. Guard and handler disagreeing
// about the scope is a bypass in one direction and an outage in the other.
//
// So the mapping is explicit, and an unrecognised path under /memory/ resolves
// to the agent tier: a route added later is refused for non-boot members until
// someone teaches this function where its scope lives. That is the safe
// direction to be wrong in, and TestMemoryRequestScope_MatchesHandlerSource
// pins each route's source so a handler that changes it breaks the test rather
// than the guard.
//
// Every uncertain case resolves to the agent tier — unparseable JSON, an
// oversized body, an unknown scope value, "both" (which genuinely reads the
// agent tier). Guessing "crew" would skip the check; guessing "agent" costs an
// invalid request a 403 instead of a 400.
func memoryRequestScope(r *http.Request) string {
	switch r.URL.Path {
	case "/memory/read", "/memory/status", "/memory/reindex":
		return normalizeMemoryScope(r.URL.Query().Get("scope"))
	case "/memory/write", "/memory/search":
		return memoryBodyScope(r)
	default:
		return "agent"
	}
}

// memoryBodyScope reads the scope out of a JSON body, buffering and restoring
// it so the handler still decodes the request normally.
func memoryBodyScope(r *http.Request) string {
	if r.Body == nil {
		return "agent"
	}
	// +1 so an oversized body is detected rather than silently truncated into
	// a parse that yields the wrong scope.
	raw, err := io.ReadAll(io.LimitReader(r.Body, sidecarMaxBodyBytes+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || int64(len(raw)) > sidecarMaxBodyBytes {
		return "agent"
	}
	var probe struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "agent"
	}
	return normalizeMemoryScope(probe.Scope)
}

func normalizeMemoryScope(scope string) string {
	if scope == memoryScopeCrew {
		return memoryScopeCrew
	}
	return "agent"
}

// writeMemoryRefusal logs the refusal and writes it in the shape the route
// expects — a JSON-RPC error envelope for the MCP transport, plain JSON for the
// legacy routes — then reports true so the caller returns immediately.
func (s *Server) writeMemoryRefusal(w http.ResponseWriter, r *http.Request, clientMsg, logMsg, actorSlug string) bool {
	if s.logger != nil {
		args := []any{"method", r.Method, "path", r.URL.Path}
		if actorSlug != "" {
			args = append(args, "acting_slug", actorSlug, "boot_slug", s.memoryAgentSlug)
		}
		s.logger.Warn(logMsg, args...)
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
