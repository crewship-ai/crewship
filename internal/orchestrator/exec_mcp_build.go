package orchestrator

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
)

func buildMCPConfig(servers []MCPServerConfig) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	mcpConfig := make(map[string]map[string]interface{})
	for _, s := range servers {
		switch s.Transport {
		case "streamable-http", "http":
			if s.Endpoint == "" {
				continue
			}
			entry := map[string]interface{}{
				"type": "http",
				"url":  s.Endpoint,
			}
			if s.Credential != nil && s.Credential.PlainValue != "" {
				headers := map[string]string{}
				switch s.Credential.Type {
				case "bearer":
					headers["Authorization"] = "Bearer " + s.Credential.PlainValue
				case "api_key":
					header := s.Credential.Header
					if header == "" {
						header = "X-API-Key"
					}
					headers[header] = s.Credential.PlainValue
				case "basic":
					headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(s.Credential.PlainValue))
				}
				if len(headers) > 0 {
					entry["headers"] = headers
				}
			}
			mcpConfig[s.Name] = entry
		case "stdio":
			if s.Command == "" {
				continue
			}
			entry := map[string]interface{}{
				"type":    "stdio",
				"command": s.Command,
			}
			if len(s.Args) > 0 {
				entry["args"] = s.Args
			}
			env := make(map[string]string)
			for k, v := range s.Env {
				env[k] = v
			}
			// Inject credential as env var for stdio servers
			if s.Credential != nil && s.Credential.PlainValue != "" {
				envVar := s.Credential.Header // reuse Header field as env var name for stdio
				if envVar == "" {
					envVar = "MCP_TOKEN"
				}
				env[envVar] = s.Credential.PlainValue
			}
			if len(env) > 0 {
				entry["env"] = env
			}
			mcpConfig[s.Name] = entry
		}
	}
	if len(mcpConfig) == 0 {
		return "", nil
	}
	// Claude Code expects {"mcpServers": {...}} wrapper
	wrapper := map[string]interface{}{"mcpServers": mcpConfig}
	b, err := json.Marshal(wrapper)
	if err != nil {
		return "", fmt.Errorf("marshal MCP config: %w", err)
	}
	return string(b), nil
}

// setupClaudeConfig writes only the non-secret Claude Code configuration
// into the container to skip onboarding prompts. Credentials are passed
// ONLY via env vars (CLAUDE_CODE_OAUTH_TOKEN) -- never written to disk.
// This prevents credential theft via prompt injection reading filesystem.

var mcpNameUnsafeRE = regexp.MustCompile(`[^a-zA-Z0-9._@-]`)

// sanitizeMCPName restricts a server or package name to a safe basename,
// preventing path traversal and shell metacharacter injection.
func sanitizeMCPName(name string) string {
	// Take only the last path component.
	name = path.Base(name)
	// Remove any characters that aren't alphanumeric, dash, underscore, dot, or @.
	safe := mcpNameUnsafeRE.ReplaceAllString(name, "")
	if safe == "" || safe == "." || safe == ".." {
		safe = "mcp-server"
	}
	return safe
}

// shellEscape replaces single quotes in a string so it can be safely used
// inside single-quoted shell arguments.
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\"'\"'")
}

// mergeMCPConfigs merges crew-level and agent-level .mcp.json configs.
// Agent servers with the same name override crew servers; different names are combined.

func mergeMCPConfigs(crewJSON, agentJSON string) (string, error) {
	type mcpConfigWrapper struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}

	merged := make(map[string]json.RawMessage)

	// Parse crew config (base layer)
	if crewJSON != "" {
		var crew mcpConfigWrapper
		if err := json.Unmarshal([]byte(crewJSON), &crew); err != nil {
			return "", fmt.Errorf("parse crew MCP config: %w", err)
		}
		for k, v := range crew.MCPServers {
			merged[k] = v
		}
	}

	// Parse agent config (override layer — same-name servers win)
	if agentJSON != "" {
		var agent mcpConfigWrapper
		if err := json.Unmarshal([]byte(agentJSON), &agent); err != nil {
			return "", fmt.Errorf("parse agent MCP config: %w", err)
		}
		for k, v := range agent.MCPServers {
			merged[k] = v
		}
	}

	if len(merged) == 0 {
		return "", nil
	}

	wrapper := map[string]interface{}{"mcpServers": merged}
	b, err := json.Marshal(wrapper)
	if err != nil {
		return "", fmt.Errorf("marshal merged MCP config: %w", err)
	}
	return string(b), nil
}

// setupSystemPromptFiles writes CLI-specific system prompt files into the container.
// OpenCode reads AGENTS.md from the working directory for system instructions.
// This ensures all CLI adapters receive the system prompt, not just Claude Code.
