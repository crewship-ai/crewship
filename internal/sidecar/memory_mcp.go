package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

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
	ID      json.RawMessage    `json:"id"`
	Result  json.RawMessage    `json:"result,omitempty"`
	Error   *memoryMCPRPCError `json:"error,omitempty"`
}

// mcpNullID is the canonical "id: null" payload required by JSON-RPC 2.0
// when the server cannot determine the request id (parse error, invalid
// envelope). Without this constant, ID was previously emitted with
// `omitempty` which produced `{}` and tripped strict clients.
var mcpNullID = json.RawMessage("null")

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
	s.handleMemoryMCPForAgent(w, r, "")
}

// handleMemoryMCPForAgent is handleMemoryMCP with an explicit caller
// identity. agentSlug "" means "the agent the sidecar was configured
// for" (the legacy bare /mcp/memory path). A non-empty slug must
// resolve to a crew member; unknown or path-hostile slugs are refused
// before any request parsing so they can never influence a path join.
func (s *Server) handleMemoryMCPForAgent(w http.ResponseWriter, r *http.Request, agentSlug string) {
	// #812: the URL slug is caller-supplied and any crew member sharing this
	// container could hit /mcp/memory/<sibling>. When a per-agent bearer token
	// is present it is authoritative — the acting agent's slug from the token
	// wins and the URL slug becomes a cross-check (warn on mismatch). A token
	// that matches no crew member is a forgery and is refused before any path
	// resolution. With no token we keep the CRE-137 URL-slug behaviour so
	// legacy adapters (and solo containers) still work.
	//
	// #1254 item A: the token-less case was the hole. /query and /escalate
	// already refuse a request that carries no Authorization header once the
	// crew has per-agent tokens (a sibling dropping the header to fall through
	// to the spoofable slug); this path did not, so a sibling could read or
	// overwrite ANY crew member's memory tier just by naming it in the URL.
	//
	// CRE-153: the refusal now lives in buildHandler, in front of the whole
	// /memory + /mcp/memory prefix — #1274 put it here only, and the five
	// legacy /memory/* routes stayed wide open. This call is the redundant
	// second line for direct invocations of the handler (tests, any future
	// in-process caller that bypasses the router); the router gate is the
	// one that actually holds the surface.
	if s.refuseUnauthorizedMemory(w, r) {
		return
	}
	effectiveSlug := agentSlug
	if actorID, actorSlug, present, ok := s.actingIdentity(r); present {
		if !ok {
			// 403, not 200. refuseUnauthorizedMemory above already answers this
			// exact condition (present && !ok) with 403, so this branch is dead
			// for every caller today — router or in-process. It stays as the
			// belt to that suspenders, and a fallback that disagrees with the
			// primary about the STATUS is worse than no fallback: a refusal
			// returning 200 is invisible in access logs, which is precisely the
			// defect /security/threat-model claims is fixed. The JSON-RPC error
			// envelope is preserved for protocol clients.
			writeJSONResponse(w, http.StatusForbidden, memoryMCPResponse{
				JSONRPC: "2.0",
				ID:      mcpNullID,
				Error:   &memoryMCPRPCError{Code: -32001, Message: "unrecognized agent token"},
			})
			return
		}
		// A roster entry whose token matches but whose Slug is empty must NOT
		// fall through to memoryAgentContextFor(""), which reads "" as "the
		// sidecar's own agent" and hands back the BOOT agent's context — so a
		// slugless member would be silently promoted to the boot agent and
		// read its private tier. Unreachable today (agents.slug is NOT NULL and
		// every create path validates it), but "" means two different things to
		// the resolver and only one of them is safe.
		if actorSlug == "" {
			s.logger.Warn("memory mcp: refusing a token that resolves to an empty agent slug",
				"acting_agent_id", actorID)
			writeJSONResponse(w, http.StatusForbidden, memoryMCPResponse{
				JSONRPC: "2.0",
				ID:      mcpNullID,
				Error:   &memoryMCPRPCError{Code: -32001, Message: "agent identity has no slug"},
			})
			return
		}
		if agentSlug != "" && agentSlug != actorSlug {
			s.logger.Warn("memory mcp: url slug does not match authenticated agent, using token identity",
				"url_slug", agentSlug, "acting_slug", actorSlug, "acting_agent_id", actorID)
		}
		effectiveSlug = actorSlug
	}
	ac, acErr := s.memoryAgentContextFor(effectiveSlug)
	if acErr != nil {
		writeJSONResponse(w, http.StatusOK, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpNullID,
			Error:   &memoryMCPRPCError{Code: -32602, Message: acErr.Error()},
		})
		return
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap — MCP requests are tiny
	if err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpNullID,
			Error:   &memoryMCPRPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	var req memoryMCPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      mcpNullID,
			Error:   &memoryMCPRPCError{Code: -32700, Message: "invalid JSON: " + err.Error()},
		})
		return
	}
	if req.JSONRPC != "2.0" {
		id := req.ID
		if len(id) == 0 {
			id = mcpNullID
		}
		writeJSONResponse(w, http.StatusBadRequest, memoryMCPResponse{
			JSONRPC: "2.0",
			ID:      id,
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
		s.respondMemoryMCPToolsCall(w, r, req, ac)
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

func (s *Server) respondMemoryMCPToolsCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, ac memory.AgentContext) {
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

	dispatcher := memory.NewDispatcher(ac)
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

// memorySlugPattern is the only shape a per-agent memory slug may take.
// Slugs are CUID-era kebab identifiers; anything else (path separators,
// dots, percent-escapes) is rejected before it can reach a path join.
var memorySlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// memoryAgentContextFor resolves the AgentContext for a crew member's
// slug. Empty slug (or the sidecar's own configured slug) returns the
// default context unchanged. Any other slug must (a) match the strict
// slug shape and (b) be a known crew member — then its memory dir is
// the sibling of the configured BasePath: <agents-root>/<slug>/.memory.
func (s *Server) memoryAgentContextFor(slug string) (memory.AgentContext, error) {
	ac := s.memoryAgentContext()
	if slug == "" || slug == s.memoryAgentSlug {
		return ac, nil
	}
	member, dir, err := s.peerCrewMember(slug)
	if err != nil {
		return memory.AgentContext{}, err
	}
	ac.AgentMemoryDir = dir
	ac.AgentID = member.ID
	return ac, nil
}

// peerCrewMember resolves slug to a known crew member + its own memory
// directory, the sibling of the configured BasePath:
// <agents-root>/<slug>/.memory. Shared by memoryAgentContextFor (MCP
// surface) and peerMemoryEngineFor (legacy FTS5 engine cache, #1301) so
// slug validation and path derivation can't drift between the two
// surfaces. slug must be non-empty and not the sidecar's own configured
// slug — callers handle that case themselves (it needs no lookup).
func (s *Server) peerCrewMember(slug string) (*CrewMember, string, error) {
	if !memorySlugPattern.MatchString(slug) {
		return nil, "", fmt.Errorf("memory: invalid agent slug %q", slug)
	}
	var member *CrewMember
	for i := range s.crewMembers {
		if s.crewMembers[i].Slug == slug {
			member = &s.crewMembers[i]
			break
		}
	}
	if member == nil {
		return nil, "", fmt.Errorf("memory: unknown agent slug %q (not a crew member)", slug)
	}
	if s.agentMemoryBase == "" {
		return nil, "", fmt.Errorf("memory: not configured on this sidecar")
	}
	// BasePath is <agents-root>/<own-slug>/.memory — hop two levels up
	// to the agents root and join the member's slug. The slug pattern
	// above guarantees the join can't traverse.
	agentsRoot := filepath.Dir(filepath.Dir(s.agentMemoryBase))
	return member, filepath.Join(agentsRoot, slug, ".memory"), nil
}

// legacyMemoryEffectiveSlug resolves which agent tier the five legacy
// /memory/* routes should serve for scope=agent (#1301: previously always
// the boot agent's). By the time a legacy handler runs, refuseUnauthorizedMemory
// has already refused a token-less downgrade, a forged token, AND a token
// resolving to an empty slug, so only two cases reach here: a valid per-agent
// token (serve THAT agent's own tier) or no token at all on a crew with no
// tokens provisioned (legacy / un-upgraded deployment — keep serving the boot
// agent's tier, "").
//
// The error return is the belt to that chokepoint: a token that maps to a
// roster entry with an EMPTY slug must not fall through to
// peerMemoryEngineFor(""), which reads "" as the boot agent — the same silent
// promotion handleMemoryMCPForAgent refuses inline on the MCP transport. Any
// in-process caller that bypasses the router fails closed here instead.
func (s *Server) legacyMemoryEffectiveSlug(r *http.Request) (string, error) {
	if actorID, actorSlug, present, ok := s.actingIdentity(r); present && ok {
		if actorSlug == "" {
			return "", fmt.Errorf("memory: agent identity %s has no slug", actorID)
		}
		return actorSlug, nil
	}
	return "", nil
}

// peerMemoryEngineFor returns the FTS5 engine + base path for slug's OWN
// agent tier, constructing and caching it on first access. Empty slug (or
// the sidecar's own configured slug) returns the existing boot-agent engine
// with no cache involved — zero behaviour change for the boot agent or a
// legacy/un-upgraded (no-tokens) deployment.
//
// The cache is bounded by crew roster size (peerCrewMember refuses any slug
// that isn't a known crew member before an entry is ever created), so no
// eviction policy is needed — every entry lives for the sidecar's process
// lifetime and is closed once at shutdown (closePeerMemoryEngines).
//
// Construction happens OUTSIDE peerMemoryEnginesMu (double-checked): the
// first access pays MkdirAll + a SQLite open + a full reindex of that tier,
// and holding the global mutex across all of it serialized every legacy
// memory operation of every agent behind one slow cold start (#1341
// follow-up). Two racing first accesses may both build an engine for the
// same slug; the loser is closed and the winner's cached instance is
// returned, so callers always share one engine per slug. ctx bounds only
// the initial reindex — a cancelled request still caches the engine (the
// index catches up on the next reindex), matching the previous behaviour
// where a failed initial reindex was logged and the engine kept.
func (s *Server) peerMemoryEngineFor(ctx context.Context, slug string) (engine *memory.Engine, basePath string, err error) {
	if slug == "" || slug == s.memoryAgentSlug {
		return s.memoryEngine, s.agentMemoryBase, nil
	}
	_, dir, err := s.peerCrewMember(slug)
	if err != nil {
		return nil, "", err
	}

	s.peerMemoryEnginesMu.Lock()
	if eng, ok := s.peerMemoryEngines[slug]; ok {
		s.peerMemoryEnginesMu.Unlock()
		return eng, dir, nil
	}
	s.peerMemoryEnginesMu.Unlock()

	// Mirrors NewServer's own boot-time engine init (server.go): the
	// directory may not exist yet on a freshly provisioned crew, and
	// SQLite errors SQLITE_CANTOPEN rather than creating parents.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("memory: create peer memory dir for %q: %w", slug, err)
	}
	eng, err := memory.New(dir, memory.DefaultConfig())
	if err != nil {
		return nil, "", fmt.Errorf("memory: init peer engine for %q: %w", slug, err)
	}
	if err := eng.ReindexContext(ctx); err != nil {
		s.logger.Warn("peer memory initial reindex failed", "error", err, "slug", slug)
	}

	s.peerMemoryEnginesMu.Lock()
	if existing, ok := s.peerMemoryEngines[slug]; ok {
		// Lost the construction race — another request cached its engine
		// while we were building ours. Serve the winner; close the loser
		// outside the lock so a slow SQLite close can't stall the cache.
		s.peerMemoryEnginesMu.Unlock()
		if cerr := eng.Close(); cerr != nil {
			s.logger.Warn("peer memory engine close after lost construction race failed",
				"error", cerr, "slug", slug)
		}
		return existing, dir, nil
	}
	if s.peerMemoryEngines == nil {
		s.peerMemoryEngines = make(map[string]*memory.Engine)
	}
	s.peerMemoryEngines[slug] = eng
	s.peerMemoryEnginesMu.Unlock()
	return eng, dir, nil
}

// closePeerMemoryEngines shuts down every cached peer engine at sidecar
// stop, alongside memoryEngine/crewMemoryEngine. Safe to call even when the
// cache is nil or empty (tests that build a Server by hand rather than via
// NewServer never populate it).
func (s *Server) closePeerMemoryEngines() {
	s.peerMemoryEnginesMu.Lock()
	defer s.peerMemoryEnginesMu.Unlock()
	for slug, eng := range s.peerMemoryEngines {
		if err := eng.Close(); err != nil {
			s.logger.Warn("peer memory engine close failed", "error", err, "slug", slug)
		}
	}
}
