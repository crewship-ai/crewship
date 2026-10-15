package orchestrator

import "encoding/json"

const IssuesMCPServerName = "crewship-issues"

func issuesMCPSpec() mcpSpec {
	return mcpSpec{Name: IssuesMCPServerName, URL: "http://127.0.0.1:9119/mcp/issues", Headers: map[string]string{"Authorization": "Bearer ${CREWSHIP_AGENT_TOKEN}"}, Transport: "http"}
}
func injectIssuesMCP(in []mcpSpec) []mcpSpec {
	for _, spec := range in {
		if spec.Name == IssuesMCPServerName {
			return in
		}
	}
	out := append([]mcpSpec{}, in...)
	return append(out, issuesMCPSpec())
}
func injectIssuesMCPIntoClaudeJSON(in string) (string, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(in), &doc); err != nil {
		return in, err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	servers := map[string]json.RawMessage{}
	if raw, exists := doc["mcpServers"]; exists {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return in, err
		}
		if servers == nil {
			servers = map[string]json.RawMessage{}
		}
	}
	if _, exists := servers[IssuesMCPServerName]; exists {
		return in, nil
	}
	spec := issuesMCPSpec()
	entry, err := json.Marshal(map[string]any{"type": "http", "url": spec.URL, "headers": spec.Headers, "alwaysLoad": true})
	if err != nil {
		return in, err
	}
	servers[IssuesMCPServerName] = entry
	encoded, err := json.Marshal(servers)
	if err != nil {
		return in, err
	}
	doc["mcpServers"] = encoded
	out, err := json.Marshal(doc)
	return string(out), err
}
