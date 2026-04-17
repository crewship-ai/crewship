package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// nodeJSLauncher extracts the first token from a command string and returns it
// only if it is exactly "npx" or "npm". Returns empty string otherwise.
func nodeJSLauncher(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "npx", "npm":
		return parts[0]
	default:
		return ""
	}
}

// isNpxCommand returns true if the command's first token is exactly "npx" or "npm".
func isNpxCommand(cmd string) bool {
	return nodeJSLauncher(cmd) != ""
}

// filterNpxServers checks whether npx/npm is available in the container and removes
// stdio servers that require them if missing. Returns the filtered list.
func filterNpxServers(ctx context.Context, container provider.ContainerProvider, containerID string, servers []MCPServerConfig, logger *slog.Logger) []MCPServerConfig {
	// 1. Check if any server uses npx/npm — if none, return unchanged.
	hasNodeLauncher := false
	for _, s := range servers {
		if s.Transport == "stdio" && isNpxCommand(s.Command) {
			hasNodeLauncher = true
			break
		}
	}
	if !hasNodeLauncher {
		return servers
	}

	// 2. Collect unique launchers needed (only "npx" or "npm" are allowed).
	// Value meaning: true = confirmed available, false = confirmed missing.
	// Launchers with probe errors are removed from the map (kept by default).
	launchers := map[string]bool{}
	for _, s := range servers {
		if s.Transport == "stdio" {
			if l := nodeJSLauncher(s.Command); l != "" {
				launchers[l] = false
			}
		}
	}

	// 3. Probe each launcher with a fixed, safe command (no interpolation).
	probeCommands := map[string][]string{
		"npx": {"sh", "-c", "command -v npx >/dev/null 2>&1 && echo ok"},
		"npm": {"sh", "-c", "command -v npm >/dev/null 2>&1 && echo ok"},
	}
	for launcher := range launchers {
		probe, ok := probeCommands[launcher]
		if !ok {
			// Unknown launcher — should not happen due to nodeJSLauncher filter,
			// but keep the server by removing from the map (not filtering it out).
			delete(launchers, launcher)
			continue
		}
		cfg := provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         probe,
			User:        "1001:1001",
		}
		result, err := container.Exec(ctx, cfg)
		if err != nil {
			// Exec failure (container not ready, timeout, etc.) — don't drop the
			// server; remove from map so it won't be filtered out.
			logger.Warn("probe exec failed, keeping servers that require "+launcher,
				"error", err,
				"container_id", containerID[:min(12, len(containerID))])
			delete(launchers, launcher)
			continue
		}
		output, _ := io.ReadAll(result.Reader)
		result.Reader.Close()
		if strings.TrimSpace(string(output)) != "" {
			launchers[launcher] = true
		}
	}

	// If all remaining launchers available, return unchanged.
	allAvailable := true
	for _, available := range launchers {
		if !available {
			allAvailable = false
			break
		}
	}
	if allAvailable {
		return servers
	}

	// 4. Filter out servers whose launcher is confirmed missing.
	var skipped []string
	var filtered []MCPServerConfig
	for _, s := range servers {
		if s.Transport == "stdio" {
			if l := nodeJSLauncher(s.Command); l != "" {
				if available, probed := launchers[l]; probed && !available {
					skipped = append(skipped, s.Name)
					continue
				}
			}
		}
		filtered = append(filtered, s)
	}
	if len(skipped) == 0 {
		return servers
	}
	logger.Warn("launcher not found in container, skipping stdio MCP servers",
		"skipped_servers", skipped,
		"container_id", containerID[:min(12, len(containerID))])
	return filtered
}

// filterMergedMCPConfigNpx parses a merged .mcp.json config, checks if npx is available
// in the container, and removes stdio servers that require npx/npm if it's missing.
// Returns the (possibly filtered) JSON string and a list of skipped server names.
func filterMergedMCPConfigNpx(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	mcpJSON string,
	logger *slog.Logger,
) (string, []string) {
	if mcpJSON == "" {
		return mcpJSON, nil
	}

	type serverEntry struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type wrapper struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	var w wrapper
	if err := json.Unmarshal([]byte(mcpJSON), &w); err != nil {
		return mcpJSON, nil
	}

	// Build MCPServerConfig slice so we can reuse filterNpxServers.
	var configs []MCPServerConfig
	nameOrder := make([]string, 0, len(w.MCPServers))
	parseFailed := make(map[string]bool)
	for name, raw := range w.MCPServers {
		nameOrder = append(nameOrder, name)
		var entry serverEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			parseFailed[name] = true
			continue
		}
		configs = append(configs, MCPServerConfig{Name: name, Transport: entry.Type, Command: entry.Command})
	}

	filtered := filterNpxServers(ctx, container, containerID, configs, logger)

	// If nothing was removed, return original JSON unchanged.
	if len(filtered) == len(configs) {
		return mcpJSON, nil
	}

	// Build set of kept names and collect skipped names.
	// Preserve entries that failed to parse — they weren't filtered by npx logic.
	kept := make(map[string]bool, len(filtered))
	for _, s := range filtered {
		kept[s.Name] = true
	}
	var skipped []string
	for _, name := range nameOrder {
		if !kept[name] && !parseFailed[name] {
			delete(w.MCPServers, name)
			skipped = append(skipped, name)
		}
	}
	if len(w.MCPServers) == 0 {
		return "", skipped
	}
	out := map[string]interface{}{"mcpServers": w.MCPServers}
	b, err := json.Marshal(out)
	if err != nil {
		logger.Error("failed to re-marshal MCP config after npx filtering", "error", err)
		return mcpJSON, nil
	}
	return string(b), skipped
}

// buildMCPConfig converts resolved MCP server configs into Claude Code's --mcp-config JSON format.
// Supports both HTTP (remote) and stdio (local npm/pip) MCP servers.
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
func setupClaudeConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	agentSlug string,
	logger *slog.Logger,
) error {
	homeDir := fmt.Sprintf("/crew/agents/%s", agentSlug)
	script := fmt.Sprintf(`mkdir -p %s/.claude && `+
		`cat > %s/.claude.json << 'CFGEOF'
{"hasCompletedOnboarding":true,"hasAvailableSubscription":true,"autoUpdates":false}
CFGEOF
chmod 600 %s/.claude.json`, homeDir, homeDir, homeDir)

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write claude config: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Debug("claude config injected (no credentials on disk)", "container_id", containerID[:min(12, len(containerID))])
	return nil
}

// setupMCPConfig writes the MCP server configuration file into the container
// so Claude Code can discover external tools via --mcp-config flag.
//
// Primary path: merge crew + agent raw .mcp.json configs. Credentials stay as
// ${VAR} references — Claude Code expands them from the container env vars.
//
// Fallback path: build from resolved MCPServerConfig entries (legacy per-binding model
// where credentials are decrypted and injected into the JSON).
func setupMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	agentSlug string,
	crewMCPJSON string,
	agentMCPJSON string,
	servers []MCPServerConfig,
	logger *slog.Logger,
) error {
	var mcpJSON string
	hadMCPInput := crewMCPJSON != "" || agentMCPJSON != "" || len(servers) > 0

	// Primary path: raw JSON configs from crew/agent DB fields
	usedPrimaryPath := false
	if crewMCPJSON != "" || agentMCPJSON != "" {
		usedPrimaryPath = true
		merged, err := mergeMCPConfigs(crewMCPJSON, agentMCPJSON)
		if err != nil {
			return fmt.Errorf("merge MCP configs: %w", err)
		}
		// Check if any stdio servers in merged config require npx/npm.
		// If all servers are filtered out, filtered will be "" and we skip
		// writing the config file entirely (do not fall through to legacy).
		filtered, _ := filterMergedMCPConfigNpx(ctx, container, containerID, merged, logger)
		mcpJSON = filtered
	}

	// Fallback: legacy per-binding model (only when primary path was not used)
	if !usedPrimaryPath && mcpJSON == "" && len(servers) > 0 {
		servers = filterNpxServers(ctx, container, containerID, servers, logger)

		var err error
		mcpJSON, err = buildMCPConfig(servers)
		if err != nil {
			return fmt.Errorf("build MCP config: %w", err)
		}
	}

	if mcpJSON == "" {
		if !hadMCPInput {
			return nil
		}
		// MCP servers were configured but all got filtered out (e.g. npx unavailable).
		// Write an empty config so --mcp-config doesn't point at a missing file.
		mcpJSON = `{"mcpServers":{}}`
	}

	homeDir := fmt.Sprintf("/crew/agents/%s", agentSlug)
	// Write config file (600 perms, owned by agent user).
	// Use base64 encoding to prevent shell injection if mcpJSON contains
	// the heredoc delimiter or other special characters.
	mcpB64 := base64.StdEncoding.EncodeToString([]byte(mcpJSON))
	script := fmt.Sprintf("echo '%s' | base64 -d > %s/.mcp.json && chmod 600 %s/.mcp.json",
		mcpB64, homeDir, homeDir)

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}
	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write MCP config: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Debug("MCP config injected", "container_id", containerID[:min(12, len(containerID))])
	return nil
}

// injectMCPOAuthTokens writes tokens.json files into the container for MCP
// servers that use OAUTH2 credentials.  This allows MCP servers (which expect
// local token files) to use tokens obtained through Crewship's OAuth flow
// without requiring the user to re-authenticate inside the container.
//
// Supports multiple OAuth MCP servers — each gets its own token from the
// matching _OAUTH_ACCESS_TOKEN:<credID> credential.
func injectMCPOAuthTokens(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID, agentSlug string,
	mcpServers []MCPServerConfig,
	credentials []Credential,
	logger *slog.Logger,
) error {
	// Group OAuth access tokens by credential ID.
	oauthTokens := make(map[string]string) // credID → plaintext access token
	for _, c := range credentials {
		if strings.HasPrefix(c.EnvVarName, "_OAUTH_ACCESS_TOKEN:") && c.PlainValue != "" {
			credID := strings.TrimPrefix(c.EnvVarName, "_OAUTH_ACCESS_TOKEN:")
			oauthTokens[credID] = c.PlainValue
		}
	}
	if len(oauthTokens) == 0 {
		return nil
	}

	// Build a map: credential ID → which servers use it (via OAUTH2 credential refs).
	credForServer := make(map[string]string) // serverName → credID
	for _, c := range credentials {
		if c.Type == "OAUTH2" && !strings.HasPrefix(c.EnvVarName, "_OAUTH_ACCESS_TOKEN:") {
			// This is a CLIENT_ID or CLIENT_SECRET ref — find which server uses it.
			// Match only exact env var references: "${ENV_VAR_NAME}" or literal "ENV_VAR_NAME".
			for _, srv := range mcpServers {
				for _, v := range srv.Env {
					if v == c.EnvVarName || v == "${"+c.EnvVarName+"}" {
						credForServer[srv.Name] = c.ID
					}
				}
			}
		}
	}

	homeDir := path.Join("/crew/agents", agentSlug)

	for _, srv := range mcpServers {
		if srv.Name == "" {
			continue
		}

		// Find the access token for this server.
		var accessToken string
		if credID, ok := credForServer[srv.Name]; ok {
			accessToken = oauthTokens[credID]
		}
		// Fallback: only use an unambiguous token when exactly one OAuth credential exists.
		if accessToken == "" && len(oauthTokens) == 1 {
			for _, tok := range oauthTokens {
				accessToken = tok
			}
		}
		if accessToken == "" {
			continue
		}

		// Derive config dir name from npm package args.
		configDirName := sanitizeMCPName(srv.Name)
		for _, arg := range srv.Args {
			if strings.Contains(arg, "/") || strings.HasPrefix(arg, "@") {
				parts := strings.Split(arg, "/")
				configDirName = sanitizeMCPName(parts[len(parts)-1])
			}
		}

		// Write tokens to BOTH the package-name dir and the server-name dir,
		// since different MCP servers look in different locations.
		srvNameSafe := sanitizeMCPName(srv.Name)
		tokenPaths := []string{
			path.Join(homeDir, ".config", configDirName, "tokens.json"),
		}
		if configDirName != srvNameSafe {
			tokenPaths = append(tokenPaths, path.Join(homeDir, ".config", srvNameSafe, "tokens.json"))
		}

		// Standard OAuth token file format — compatible with most MCP servers.
		tokensJSON, _ := json.Marshal(map[string]interface{}{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expiry_date":  9999999999999,
		})

		tokB64 := base64.StdEncoding.EncodeToString(tokensJSON)

		for _, tp := range tokenPaths {
			dir := path.Dir(tp)
			// Use shell-safe quoting to prevent injection via crafted server names.
			script := fmt.Sprintf(
				"mkdir -p '%s' && printf '%%s' '%s' | base64 -d > '%s' && chmod 600 '%s'",
				shellEscape(dir), tokB64, shellEscape(tp), shellEscape(tp),
			)
			cfg := provider.ExecConfig{
				ContainerID: containerID,
				Cmd:         []string{"sh", "-c", script},
				User:        "1001:1001",
			}
			result, err := container.Exec(ctx, cfg)
			if err != nil {
				logger.Warn("write MCP OAuth tokens", "server", srv.Name, "path", tp, "error", err)
				continue
			}
			io.Copy(io.Discard, result.Reader)
			result.Reader.Close()
			logger.Debug("MCP OAuth tokens injected", "server", srv.Name, "path", tp)
		}
	}

	return nil
}

// mcpNameUnsafeRE matches characters that must be stripped from MCP server/package
// names. Hoisted to package level so it compiles once at init, not per call.
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
func setupSystemPromptFiles(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	systemPrompt := crewshipSystemPreamble + req.SystemPrompt

	var script string

	switch req.CLIAdapter {
	case "OPENCODE":
		// OpenCode reads AGENTS.md from the project root / CWD for instructions.
		// Use base64 encoding to avoid heredoc delimiter injection.
		encoded := base64.StdEncoding.EncodeToString([]byte(systemPrompt))
		script = fmt.Sprintf("echo %s | base64 -d > AGENTS.md", encoded)

	default:
		return nil
	}

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		WorkingDir:  workDir,
		User:        "1001:1001",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write system prompt files for %s: %w", req.CLIAdapter, err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Debug("system prompt files written", "cli_adapter", req.CLIAdapter, "container_id", containerID[:min(12, len(containerID))])
	return nil
}
