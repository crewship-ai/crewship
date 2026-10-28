package orchestrator

// This host-only selection restricts the original agent CLI to one fixed MCP
// server. It grants no document authority: every operation needs the current
// confirmed attempt and original human mandate checked by the host.
const nativeCraftMCP = `{"mcpServers":{"crewship-craft":{"type":"http","url":"http://127.0.0.1:9119/mcp/craft-native","headers":{"Authorization":"Bearer ${CREWSHIP_AGENT_TOKEN}"},"alwaysLoad":true}}}`

func (r AgentRunRequest) WithNativeCraftTools() (AgentRunRequest, error) {
	if r.mooringDocumentTools {
		return AgentRunRequest{}, ErrPrivateRunUnqualified
	}
	selected, err := r.WithMooringDocumentTools()
	if err != nil {
		return AgentRunRequest{}, err
	}
	selected.mooringDocumentTools = false
	selected.nativeCraftTools = true
	return selected, nil
}

func privateDocumentMCP(r AgentRunRequest) string {
	if r.nativeCraftTools && !r.mooringDocumentTools {
		return nativeCraftMCP
	}
	return mooringDocumentMCP
}
