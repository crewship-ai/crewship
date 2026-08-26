package orchestrator

import "encoding/json"

// RoutinesMCPServerName is the canonical name every adapter advertises for
// the in-container routine-authoring MCP server. MUST match
// internal/sidecar.RoutinesMCPServerName — both sides hard-code the string
// rather than share an import to avoid an orchestrator→sidecar cycle.
// Drift is caught by TestRoutinesMCPSpec_*.
const RoutinesMCPServerName = "crewship-routines"

// routinesMCPSidecarAddr is the loopback address the sidecar binds at
// startup — the same listener that hosts /mcp/memory. The routine tools are
// served at /mcp/routines on this address. Mirrors memoryMCPSidecarAddr;
// changing the sidecar's DefaultAddr requires updating both constants.
const routinesMCPSidecarAddr = "127.0.0.1:9119"

// routinesMCPSpec returns the canonical mcpSpec for the sidecar-hosted
// routine-authoring MCP server. Every CLI adapter that supports MCP injects
// this entry into its native config (.mcp.json, .codex/config.toml, etc.)
// alongside crewship-memory so the model sees save_routine / list_routines
// as native tool calls regardless of which CLI is driving the container.
func routinesMCPSpec() mcpSpec {
	return mcpSpec{
		Name: RoutinesMCPServerName,
		URL:  "http://" + routinesMCPSidecarAddr + "/mcp/routines",
		// #812: the per-agent bearer token, exactly as crewship-memory sends
		// it. NOT optional and NOT defence-in-depth: respondRoutinesMCPToolsCall
		// (internal/sidecar/routine_mcp.go) opens by resolving actingAgentID and
		// answers 403 {"error":"unrecognized agent token"} when the header is
		// absent, because tokensProvisioned() is true for every real container.
		// Omitting it made EVERY routine/page tool call fail — save_routine,
		// save_page, discover_capabilities, list_routines, run_routine — while
		// the tools still advertised themselves in tools/list, so the model saw
		// the capability, called it, and got a bare auth error it could only
		// report as a token problem it had no way to fix.
		Headers:   map[string]string{"Authorization": "Bearer ${CREWSHIP_AGENT_TOKEN}"},
		Transport: "http",
	}
}

// injectRoutinesMCP returns the spec list with crewship-routines appended IF
// it isn't already present. Idempotent. A user-defined entry named
// "crewship-routines" wins (we do not overwrite). Mirrors injectMemoryMCP.
func injectRoutinesMCP(in []mcpSpec) []mcpSpec {
	for _, s := range in {
		if s.Name == RoutinesMCPServerName {
			return in
		}
	}
	out := make([]mcpSpec, 0, len(in)+1)
	out = append(out, in...)
	out = append(out, routinesMCPSpec())
	return out
}

// injectRoutinesMCPIntoClaudeJSON adds the crewship-routines MCP server to a
// Claude-flavour .mcp.json document. Behaviour mirrors
// injectMemoryMCPIntoClaudeJSON: user-defined override wins; a malformed
// input returns the error so the caller can log and continue with the
// original JSON (routine tools degrade but the agent still runs).
func injectRoutinesMCPIntoClaudeJSON(in string) (string, error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(in), &doc); err != nil {
		return in, err
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]json.RawMessage{}
	}
	if _, exists := doc.MCPServers[RoutinesMCPServerName]; exists {
		return in, nil
	}
	entry := map[string]any{
		"type": "http",
		"url":  routinesMCPSpec().URL,
		// Same per-agent bearer token the memory server sends, and required
		// for the same reason — see routinesMCPSpec. Claude Code expands
		// ${VAR} in headers from the agent's env (CREWSHIP_AGENT_TOKEN).
		"headers": routinesMCPSpec().Headers,
		// alwaysLoad presents this server's tools (save_routine / list_routines
		// / run_routine) to the model EAGERLY at session start instead of
		// deferring them behind a ToolSearch discovery hop. Mirrors the memory
		// injector. Claude-Code-only field (v2.1.121+); unknown keys are ignored
		// by older CLIs, and the other adapters load MCP tools eagerly already.
		"alwaysLoad": true,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return in, err
	}
	doc.MCPServers[RoutinesMCPServerName] = entryJSON
	out, err := json.Marshal(doc)
	if err != nil {
		return in, err
	}
	return string(out), nil
}
