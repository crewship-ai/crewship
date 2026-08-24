package orchestrator

import "encoding/json"

// NotifyMCPServerName is the canonical name every adapter advertises for
// the in-container notification MCP server. MUST match
// internal/sidecar.NotifyMCPServerName — both sides hard-code the string
// rather than share an import to avoid an orchestrator→sidecar cycle.
// Drift is caught by TestNotifyMCPSpec_*.
const NotifyMCPServerName = "crewship-notify"

// notifyMCPSidecarAddr is the loopback address the sidecar binds at
// startup — the same listener that hosts /mcp/memory. The notification tools
// are served at /mcp/notify on this address. Mirrors memoryMCPSidecarAddr;
// changing the sidecar's DefaultAddr requires updating both constants.
const notifyMCPSidecarAddr = "127.0.0.1:9119"

// notifyMCPSpec returns the canonical mcpSpec for the sidecar-hosted
// notification MCP server. Every CLI adapter that supports MCP injects
// this entry into its native config (.mcp.json, .codex/config.toml, etc.)
// alongside crewship-memory so the model sees notify_send and
// list_notification_channels as native tool calls regardless of which CLI is
// driving the container.
func notifyMCPSpec() mcpSpec {
	return mcpSpec{
		Name: NotifyMCPServerName,
		URL:  "http://" + notifyMCPSidecarAddr + "/mcp/notify",
		// #812: required, not optional — respondNotifyMCPToolsCall
		// (internal/sidecar/notify_mcp.go) resolves actingAgentID before it
		// dispatches either tool and 403s "unrecognized agent token" without
		// this header. Same omission the routines server had; both are fixed
		// together because both fail the same silent way — the tool is
		// advertised, the model calls it, and the only thing coming back is an
		// auth error the model cannot act on.
		Headers:   map[string]string{"Authorization": "Bearer ${CREWSHIP_AGENT_TOKEN}"},
		Transport: "http",
	}
}

// injectNotifyMCP returns the spec list with crewship-notify appended IF
// it isn't already present. Idempotent. A user-defined entry named
// "crewship-notify" wins (we do not overwrite). Mirrors injectMemoryMCP.
func injectNotifyMCP(in []mcpSpec) []mcpSpec {
	for _, s := range in {
		if s.Name == NotifyMCPServerName {
			return in
		}
	}
	out := make([]mcpSpec, 0, len(in)+1)
	out = append(out, in...)
	out = append(out, notifyMCPSpec())
	return out
}

// injectNotifyMCPIntoClaudeJSON adds the crewship-notify MCP server to a
// Claude-flavour .mcp.json document. Behaviour mirrors
// injectMemoryMCPIntoClaudeJSON: user-defined override wins; a malformed
// input returns the error so the caller can log and continue with the
// original JSON (routine tools degrade but the agent still runs).
func injectNotifyMCPIntoClaudeJSON(in string) (string, error) {
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(in), &doc); err != nil {
		return in, err
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]json.RawMessage{}
	}
	if _, exists := doc.MCPServers[NotifyMCPServerName]; exists {
		return in, nil
	}
	entry := map[string]any{
		"type": "http",
		"url":  notifyMCPSpec().URL,
		// Per-agent bearer token — see notifyMCPSpec for why it is required.
		"headers": notifyMCPSpec().Headers,
		// alwaysLoad presents this server's tools (notify_send /
		// list_notification_channels) to the model EAGERLY at session start instead of
		// deferring them behind a ToolSearch discovery hop. Mirrors the memory
		// injector. Claude-Code-only field (v2.1.121+); unknown keys are ignored
		// by older CLIs, and the other adapters load MCP tools eagerly already.
		"alwaysLoad": true,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return in, err
	}
	doc.MCPServers[NotifyMCPServerName] = entryJSON
	out, err := json.Marshal(doc)
	if err != nil {
		return in, err
	}
	return string(out), nil
}
