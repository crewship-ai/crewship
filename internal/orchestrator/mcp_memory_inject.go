package orchestrator

import (
	"context"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/provider"
)

// MemoryMCPServerName is the canonical name every adapter advertises for
// the in-container memory MCP server. MUST match
// internal/sidecar.MemoryMCPServerName — both sides hard-code the string
// rather than share an import to avoid an orchestrator→sidecar cycle
// (sidecar already pulls orchestrator-adjacent code indirectly via
// internal/journal). Drift is caught by TestMemoryMCPSpec_*.
const MemoryMCPServerName = "crewship-memory"

// memoryMCPSidecarAddr is the loopback address+path the sidecar binds at
// startup. Mirrors sidecar.DefaultAddr ("127.0.0.1:9119") + the static
// /mcp/memory route registered in sidecar.buildHandler. Changing either
// half requires updating both — TestMemoryMCPSpec_PointsAtSidecarLoopback
// guards the prefix; the sidecar's TestMemoryMCP_* guards the route.
const memoryMCPSidecarAddr = "127.0.0.1:9119"

// memoryMCPSpec returns the canonical mcpSpec for the sidecar-hosted
// memory MCP server. Every CLI adapter that supports MCP injects this
// entry into its native config (.mcp.json, .codex/config.toml, etc.) so
// the model sees memory.read / memory.write / memory.search /
// memory.append_daily as native tool calls regardless of which CLI is
// driving the container.
//
// agentSlug scopes the URL to the calling agent: crew members share one
// sidecar, and the bare /mcp/memory path can only serve the agent the
// sidecar was STARTED for — every other member's memory calls landed in
// that first agent's .memory tier (CRE-137). A non-empty slug appends
// /mcp/memory/<slug> so the sidecar resolves the caller's own tier.
// Empty slug keeps the legacy bare path (solo containers, tests).
//
// Transport "http" means streamable-HTTP per MCP spec. All 5 supported
// CLIs (Claude/Codex/Gemini/OpenCode/Droid) honour this transport — see
// each adapter's writeMCP* for the wire-format translation.
func memoryMCPSpec(agentSlug string) mcpSpec {
	url := "http://" + memoryMCPSidecarAddr + "/mcp/memory"
	if agentSlug != "" {
		url += "/" + agentSlug
	}
	return mcpSpec{
		Name:      MemoryMCPServerName,
		URL:       url,
		Transport: "http",
		// #812: present the per-agent bearer token so the sidecar resolves the
		// ACTING agent from authentication, not the URL slug (which any crew
		// member sharing the container could point at a sibling). The ${VAR}
		// form is expanded per-adapter from the agent's env — CREWSHIP_AGENT_TOKEN
		// is set in exec_env by the orchestrator. When the env var is unset
		// (internal auth off) the sidecar simply falls back to the URL-slug path.
		Headers: map[string]string{"Authorization": "Bearer ${CREWSHIP_AGENT_TOKEN}"},
	}
}

// memorySinkReady reports whether the in-container memory sidecar is
// reachable on :9119 right now — the readiness probe (Kubernetes
// readiness-probe model) the injection gates on. It reuses checkSidecar
// so there is one liveness definition. Sidecars are container-scoped and
// persistent, so a warm crew container (any prior Full run) returns true
// even for a SkipSidecar worker — which is correct, the sink IS up. A
// cold first-touch returns false, so the memory tool is withheld.
func memorySinkReady(ctx context.Context, ctr provider.ContainerProvider, containerID string) bool {
	return checkSidecar(ctx, ctr, containerID) != nil
}

// injectMemoryMCP returns the spec list with crewship-memory appended IF
// it isn't already present. Idempotent: re-running on an output it
// already injected is a no-op. User-defined entries named
// "crewship-memory" win (we do not overwrite) so an operator who wants to
// point the name at a hub/marketplace memory server can override the
// default by declaring the name first in the crew MCP config.
// sinkReady health-gates the injection (2b — health-gated capability
// advertisement, per Circuit Breaker / "never route to a down backend").
// When the memory sidecar isn't reachable we do NOT advertise the tool:
// a model that can't see memory.write reports "I couldn't persist that"
// instead of calling a dead :9119 and getting a false success. This is
// the structural half of closing the silent-loss class — the durable-
// write handler (2a) is the other half for when the sink IS up.
func injectMemoryMCP(in []mcpSpec, agentSlug string, sinkReady bool) []mcpSpec {
	if !sinkReady {
		return in
	}
	for _, s := range in {
		if s.Name == MemoryMCPServerName {
			return in
		}
	}
	out := make([]mcpSpec, 0, len(in)+1)
	out = append(out, in...)
	out = append(out, memoryMCPSpec(agentSlug))
	return out
}

// injectMemoryMCPIntoClaudeJSON adds the crewship-memory MCP server to a
// Claude-flavour .mcp.json document (the {"mcpServers": {...}} envelope).
// Used by setupMCPConfig as a final post-processing step so we don't have
// to fork that function's npx-filter / OAuth / credential-env-expansion
// branches — we just patch the JSON right before it lands in the
// container.
//
// Behaviour:
//   - If the input already contains an entry named "crewship-memory",
//     leave it intact (user-defined override wins).
//   - If the input is malformed, return the error so the caller can log
//     and continue with the original JSON (memory tools degrade but the
//     agent still runs).
//   - The injected entry uses type="http" so Claude streams JSON-RPC over
//     HTTP to the sidecar loopback the model's CLI shares.
func injectMemoryMCPIntoClaudeJSON(in, agentSlug string, sinkReady bool) (string, error) {
	// 2b: don't advertise a tool whose backend is down (see injectMemoryMCP).
	if !sinkReady {
		return in, nil
	}
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(in), &doc); err != nil {
		return in, err
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]json.RawMessage{}
	}
	if _, exists := doc.MCPServers[MemoryMCPServerName]; exists {
		// Operator already declared a server under our reserved name —
		// leave their config alone.
		return in, nil
	}
	entry := map[string]any{
		"type": "http",
		"url":  memoryMCPSpec(agentSlug).URL,
		// #812: per-agent bearer token — Claude Code expands ${VAR} in headers
		// from the agent's env (CREWSHIP_AGENT_TOKEN). Lets the sidecar attribute
		// memory calls to the authenticated agent, not the caller-supplied slug.
		"headers": map[string]string{"Authorization": "Bearer ${CREWSHIP_AGENT_TOKEN}"},
		// alwaysLoad presents this server's tools (memory.read / write /
		// search / append_daily) to the model EAGERLY at session start
		// instead of deferring them behind a ToolSearch discovery hop. These
		// are first-party tools the agent needs almost every turn, so the
		// one-time context cost is worth eliminating a round-trip per run.
		// Claude-Code-only field (v2.1.121+); unknown keys are ignored by
		// older CLIs, and the other adapters load MCP tools eagerly already.
		"alwaysLoad": true,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return in, err
	}
	doc.MCPServers[MemoryMCPServerName] = entryJSON
	out, err := json.Marshal(doc)
	if err != nil {
		return in, err
	}
	return string(out), nil
}
