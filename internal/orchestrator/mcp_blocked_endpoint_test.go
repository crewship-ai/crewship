package orchestrator

import "testing"

// #2428: an MCP server the crew's network policy blocks must not be written
// into the agent's CLI config at all.
//
// The sidecar gateway already refuses to connect to it, so a config entry only
// makes the CLI dial a host the container proxy will refuse: Codex retries it
// three times per run and prints a fatal transport error each time, which
// reads like a Crewship fault in the agent's own output. The two sides — the
// config we write and the connections the sidecar makes — have to agree.
func TestNormaliseMCPInputs_DropsEndpointsTheCrewPolicyBlocks(t *testing.T) {
	base := AgentRunRequest{
		AgentSlug: "reviewer",
		CrewMCPConfigJSON: `{"mcpServers":{
			"linear":{"url":"https://mcp.linear.app/mcp"},
			"allowed":{"url":"https://api.anthropic.com/mcp"},
			"local":{"command":"npx","args":["-y","server"]}
		}}`,
	}

	t.Run("restricted crew keeps only the allowlisted and the stdio server", func(t *testing.T) {
		req := base
		req.NetworkMode = "restricted"
		req.AllowedDomains = []string{"api.anthropic.com"}
		specs, err := normaliseMCPInputs(req)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, s := range specs {
			got[s.Name] = true
		}
		if got["linear"] {
			t.Error("linear was written into the config even though the crew blocks mcp.linear.app")
		}
		if !got["allowed"] || !got["local"] {
			t.Errorf("dropped too much: %v", got)
		}
	})

	t.Run("free crew keeps everything", func(t *testing.T) {
		req := base
		req.NetworkMode = "free"
		specs, err := normaliseMCPInputs(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(specs) != 3 {
			t.Errorf("free mode gates nothing, got %d servers", len(specs))
		}
	})

	t.Run("an unparseable endpoint on a restricted crew is dropped", func(t *testing.T) {
		req := AgentRunRequest{
			AgentSlug:         "reviewer",
			NetworkMode:       "restricted",
			AllowedDomains:    []string{"api.anthropic.com"},
			CrewMCPConfigJSON: `{"mcpServers":{"broken":{"url":"://nope"}}}`,
		}
		specs, err := normaliseMCPInputs(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(specs) != 0 {
			t.Errorf("an endpoint we cannot parse must not be handed to a CLI: %v", specs)
		}
	})
}
