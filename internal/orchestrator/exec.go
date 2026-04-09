package orchestrator

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

var envVarNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

const crewshipSystemPreamble = `You are running inside a Crewship agent container.
Your working directory IS the output directory -- files you create or edit here are immediately visible to the user in the Files panel.

FILESYSTEM:
- HOME (~/) = /crew/agents/{your-slug}/ — persistent, personal (config, memory)
- Working dir = /output/{your-slug}/ — visible in Files panel
- Shared crew space = /crew/shared/ — all crew members can read/write
- Secrets = /secrets/{your-slug}/ — read-only credential files (one file per credential, named by env var)
- Scratch = /workspace/ — temporary, not persistent
Do NOT attempt to write outside these directories -- the filesystem is read-only elsewhere.

CREDENTIALS:
- CLI tokens and secrets are available as files in /secrets/{your-slug}/ (e.g., /secrets/{your-slug}/GH_TOKEN)
- The .env file in /secrets/{your-slug}/.env maps env var names to file paths
- API keys for LLM providers are injected automatically via the sidecar proxy
`

func BuildCLICommand(req AgentRunRequest) []string {
	switch req.CLIAdapter {
	case "CLAUDE_CODE":
		cmd := []string{
			"claude", "--print",
			"--output-format", "stream-json",
			"--include-partial-messages",
			"--dangerously-skip-permissions",
			"--verbose",
		}
		if req.LLMModel != "" {
			cmd = append(cmd, "--model", req.LLMModel)
		}
		systemPrompt := crewshipSystemPreamble + req.SystemPrompt
		cmd = append(cmd, "--system-prompt", systemPrompt)
		if req.ToolProfile == "MINIMAL" {
			cmd = append(cmd, "--tools", "Read,Search,Grep")
		}
		// Inject MCP server configuration via file (Claude Code reads --mcp-config from files)
		if len(req.MCPServers) > 0 || req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" {
			cmd = append(cmd, "--mcp-config", fmt.Sprintf("/crew/agents/%s/.mcp.json", req.AgentSlug))
		}
		// Use -- separator to prevent variadic flags (--tools) from consuming the user message
		cmd = append(cmd, "--", req.UserMessage)
		return cmd

	case "CODEX_CLI":
		cmd := []string{"codex", "--quiet"}
		if req.ToolProfile == "CODING" {
			cmd = append(cmd, "--sandbox")
		}
		cmd = append(cmd, req.UserMessage)
		return cmd

	case "GEMINI_CLI":
		cmd := []string{"gemini"}
		systemPrompt := crewshipSystemPreamble + req.SystemPrompt
		if systemPrompt != "" {
			cmd = append(cmd, "--system-instruction", systemPrompt)
		}
		cmd = append(cmd, "-p", req.UserMessage)
		return cmd

	case "OPENCODE":
		// OpenCode reads AGENTS.md from CWD for system instructions.
		// We write it via setupSystemPromptFiles() before exec.
		return []string{"opencode", "run", req.UserMessage}

	default:
		return []string{"claude", "--print", req.UserMessage}
	}
}

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

func BuildEnvVars(req AgentRunRequest, activeCred *Credential) []string {
	env := []string{
		fmt.Sprintf("HOME=/crew/agents/%s", req.AgentSlug),
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
		"CREWSHIP_CREW_SHARED=/crew/shared",
	}

	if activeCred != nil {
		envVar := resolveEnvVar(activeCred)
		env = append(env, envVar+"="+activeCred.PlainValue)
	}

	for _, cred := range req.Credentials {
		if activeCred != nil && cred.ID == activeCred.ID {
			continue
		}
		if cred.EnvVarName != "" && cred.PlainValue != "" {
			envVar := resolveEnvVar(&cred)
			alreadySet := false
			for _, e := range env {
				if len(e) > len(envVar) && e[:len(envVar)+1] == envVar+"=" {
					alreadySet = true
					break
				}
			}
			if !alreadySet {
				env = append(env, envVar+"="+cred.PlainValue)
			}
		}
	}

	return env
}

// injectMCPCredentialEnvVars ensures that credentials referenced as ${ENV_VAR}
// in MCP .mcp.json configs are available as env vars in the agent exec.
// This is needed because sidecar mode skips BuildEnvVars (credentials go via
// stdin), but MCP servers need actual env vars for ${VAR} expansion.
func injectMCPCredentialEnvVars(req AgentRunRequest, env []string) []string {
	// Collect env var names referenced in crew/agent MCP configs
	mcpEnvRefs := collectMCPEnvRefs(req.CrewMCPConfigJSON, req.AgentMCPConfigJSON)

	// Also collect from table-based MCPServers (after JSON blob migration)
	for _, srv := range req.MCPServers {
		for k, v := range srv.Env {
			if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
				mcpEnvRefs[v[2:len(v)-1]] = true
			} else if k != "" {
				// Env key itself might be the var name needed
				mcpEnvRefs[k] = true
			}
		}
	}

	if len(mcpEnvRefs) == 0 {
		return env
	}

	// Build set of already-set env var names
	existing := make(map[string]bool)
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			existing[e[:idx]] = true
		}
	}

	// Match credentials to MCP env var references
	for _, cred := range req.Credentials {
		if cred.EnvVarName == "" || cred.PlainValue == "" {
			continue
		}
		if _, needed := mcpEnvRefs[cred.EnvVarName]; !needed {
			continue
		}
		if existing[cred.EnvVarName] {
			continue
		}
		env = append(env, cred.EnvVarName+"="+cred.PlainValue)
		existing[cred.EnvVarName] = true
	}

	return env
}

// collectMCPEnvRefs parses MCP config JSONs and returns env var names
// referenced as ${VAR} in the "env" blocks of server definitions.
func collectMCPEnvRefs(configs ...string) map[string]bool {
	refs := make(map[string]bool)
	for _, cfg := range configs {
		if cfg == "" {
			continue
		}
		var wrapper struct {
			MCPServers map[string]struct {
				Env map[string]string `json:"env"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(cfg), &wrapper); err != nil {
			continue
		}
		for _, srv := range wrapper.MCPServers {
			for _, val := range srv.Env {
				// Match ${VAR_NAME} pattern
				if len(val) > 3 && val[0] == '$' && val[1] == '{' && val[len(val)-1] == '}' {
					refs[val[2:len(val)-1]] = true
				}
			}
		}
	}
	return refs
}

// BuildEnvVarsSidecar builds env vars for the agent when sidecar mode is active.
// API key credentials are NOT included -- the sidecar proxy injects them into HTTP requests.
// OAuth tokens (AI_CLI_TOKEN) are injected directly as CLAUDE_CODE_OAUTH_TOKEN because
// the sidecar cannot use them for x-api-key injection.
// When keeperEnabled is true, SECRET credentials are NOT included -- agents must
// request them via the Keeper API (/keeper/request on the sidecar).
// When keeperEnabled is false, SECRET credentials are injected as env vars directly.
// The agent gets dummy API keys and proxy configuration pointing to the sidecar.
func BuildEnvVarsSidecar(req AgentRunRequest, keeperEnabled bool) []string {
	// Check if we have an OAuth token -- this changes the env var strategy.
	// OAuth tokens use HTTPS CONNECT tunnel (sidecar just allowlists the domain).
	// Claude Code sets Authorization: Bearer itself inside the encrypted tunnel.
	// IMPORTANT: When OAuth is present, we must NOT set ANTHROPIC_API_KEY or
	// ANTHROPIC_BASE_URL because Claude Code prioritizes API key auth over OAuth
	// when both are present, and the dummy key causes authentication failure.
	hasOAuth := false
	var oauthToken string
	for _, cred := range req.Credentials {
		isOAuth := cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat")
		if isOAuth && cred.PlainValue != "" {
			hasOAuth = true
			oauthToken = cred.PlainValue
			break
		}
	}

	env := []string{
		fmt.Sprintf("HOME=/crew/agents/%s", req.AgentSlug),
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
		"CREWSHIP_CREW_SHARED=/crew/shared",
		// Proxy config -- all outbound HTTP goes through the sidecar
		"HTTP_PROXY=http://127.0.0.1:9119",
		"HTTPS_PROXY=http://127.0.0.1:9119",
		"http_proxy=http://127.0.0.1:9119",
		"https_proxy=http://127.0.0.1:9119",
		// SECURITY: NO_PROXY prevents infinite proxy loops for localhost health checks
		// and internal sidecar communication. Without this, curl/wget/Python requests
		// would try to proxy requests to 127.0.0.1 through the proxy itself.
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	}

	if hasOAuth {
		// OAuth mode: Claude Code authenticates via HTTPS CONNECT tunnel.
		// The sidecar allowlists api.anthropic.com and passes the tunnel through.
		// No ANTHROPIC_BASE_URL (let Claude Code use the default HTTPS endpoint).
		// No dummy ANTHROPIC_API_KEY (would override OAuth authentication).
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
		// Still set dummy keys for other providers (OpenAI, Google) for sidecar injection
		env = append(env, "OPENAI_API_KEY=sk-dummy-crewship-sidecar")
		env = append(env, "GOOGLE_API_KEY=dummy-crewship-sidecar")
	} else {
		// API key mode: use reverse proxy via ANTHROPIC_BASE_URL for credential injection.
		// The sidecar intercepts plain HTTP requests and injects the real API key.
		env = append(env,
			"ANTHROPIC_BASE_URL=http://127.0.0.1:9119",
			"ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar",
			"OPENAI_API_KEY=sk-dummy-crewship-sidecar",
			"GOOGLE_API_KEY=dummy-crewship-sidecar",
		)
	}

	// CLI_TOKEN credentials: injected as direct env vars (agent sees them).
	// CLI tools (gh, glab, vercel...) read credentials from env vars, not HTTP proxy.
	// The sidecar proxy cannot inject credentials into HTTPS CONNECT tunnels.
	for _, cred := range req.Credentials {
		if cred.Type == "CLI_TOKEN" && cred.EnvVarName != "" && cred.PlainValue != "" {
			env = append(env, cred.EnvVarName+"="+cred.PlainValue)
		}
	}

	// SECRET credentials: when Keeper is enabled, agents must request them via
	// the Keeper API (/keeper/request), enforcing access control + audit trail.
	// When Keeper is disabled, inject them directly as env vars (legacy mode).
	if !keeperEnabled {
		for _, cred := range req.Credentials {
			if cred.Type == "SECRET" && cred.EnvVarName != "" && cred.PlainValue != "" {
				env = append(env, cred.EnvVarName+"="+cred.PlainValue)
			}
		}
	}

	return env
}

// resolveEnvVar returns the correct env var name for a credential.
// OAuth tokens (type AI_CLI_TOKEN or value prefix sk-ant-oat) must be set as
// CLAUDE_CODE_OAUTH_TOKEN -- Claude Code ignores them in ANTHROPIC_API_KEY.
func resolveEnvVar(cred *Credential) string {
	if cred.Type == "AI_CLI_TOKEN" || strings.HasPrefix(cred.PlainValue, "sk-ant-oat") {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return cred.EnvVarName
}

// DefaultEnvVarForProvider returns the conventional env var name for a CLI tool provider.
// Used by the UI to auto-suggest the env var when assigning a credential.
func DefaultEnvVarForProvider(provider string) string {
	switch provider {
	case "GITHUB":
		return "GH_TOKEN"
	case "GITLAB":
		return "GITLAB_TOKEN"
	case "VERCEL":
		return "VERCEL_TOKEN"
	case "AWS":
		return "AWS_ACCESS_KEY_ID"
	case "KUBERNETES":
		return "KUBECONFIG"
	default:
		return ""
	}
}

// PreRunInstallPackages installs system packages as root before the agent starts.
// The agent runs as UID 1001 (non-root) and cannot install apt packages itself.
// This function runs `apt-get install` as root (UID 0), then the agent exec
// runs as UID 1001 with the packages available in PATH.
func PreRunInstallPackages(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	packages []string,
	logger *slog.Logger,
) error {
	if len(packages) == 0 {
		return nil
	}

	// Sanitize package names: only allow alphanumeric, dash, dot, plus
	for _, pkg := range packages {
		for _, c := range pkg {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '+') {
				return fmt.Errorf("invalid package name: %q", pkg)
			}
		}
	}

	script := "apt-get update -qq && apt-get install -y -qq " + strings.Join(packages, " ")
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "0:0",
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pre-run install: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Info("pre-run packages installed",
		"container_id", containerID[:min(12, len(containerID))],
		"packages", packages,
	)
	return nil
}

// writeCredentialFiles writes CLI_TOKEN and SECRET credentials as individual files
// into the agent's secrets directory. Each credential is written as a separate file
// named after its env var (e.g., /secrets/{agent-slug}/GH_TOKEN). A combined .env
// file is also generated for tools that source environment files.
// Files are written as root (UID 0) then chowned to 1001:1001 with mode 0400 (read-only).
func writeCredentialFiles(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	agentSlug string,
	creds []Credential,
	secretsAgentDir string,
	secretsSharedDir string,
	logger *slog.Logger,
) error {
	// Collect credentials that should be written as files.
	// API_KEY and AI_CLI_TOKEN are handled by the sidecar proxy — not written to disk.
	type credFile struct {
		EnvVar string
		Value  string
	}
	var files []credFile
	for _, c := range creds {
		if (c.Type == "CLI_TOKEN" || c.Type == "SECRET") && c.EnvVarName != "" && c.PlainValue != "" {
			if !envVarNameRE.MatchString(c.EnvVarName) {
				return fmt.Errorf("invalid credential env var name: %q", c.EnvVarName)
			}
			files = append(files, credFile{EnvVar: c.EnvVarName, Value: c.PlainValue})
		}
	}

	if len(files) == 0 {
		return nil
	}

	// Build a shell script that writes each credential as a file and generates .env.
	// Uses base64 encoding to prevent shell injection from credential values.
	var scriptParts []string
	var envLines []string

	for _, f := range files {
		valB64 := base64.StdEncoding.EncodeToString([]byte(f.Value))
		filePath := secretsAgentDir + "/" + f.EnvVar
		scriptParts = append(scriptParts,
			fmt.Sprintf("echo '%s' | base64 -d > %s", valB64, filePath),
			fmt.Sprintf("chown 1001:1001 %s", filePath),
			fmt.Sprintf("chmod 0400 %s", filePath),
		)
		envLines = append(envLines, f.EnvVar+"="+filePath)
	}

	// Write .env file (maps env var names to file paths, not raw values)
	envContent := strings.Join(envLines, "\n") + "\n"
	envB64 := base64.StdEncoding.EncodeToString([]byte(envContent))
	envPath := secretsAgentDir + "/.env"
	scriptParts = append(scriptParts,
		fmt.Sprintf("echo '%s' | base64 -d > %s", envB64, envPath),
		fmt.Sprintf("chown 1001:1001 %s", envPath),
		fmt.Sprintf("chmod 0400 %s", envPath),
	)

	// Chown the secrets dir itself (not recursively) and each file individually.
	// Chowning individual files rather than the parent dir prevents agents sharing
	// UID 1001 from traversing or listing sibling agents' secret directories.
	scriptParts = append(scriptParts,
		fmt.Sprintf("chown 1001:1001 %s", secretsAgentDir),
	)

	script := strings.Join(scriptParts, " && ")

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "0:0",
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write credential files: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Info("credential files written",
		"agent_slug", agentSlug,
		"secrets_dir", secretsAgentDir,
		"file_count", len(files),
	)
	return nil
}

// sidecarHealth holds the parsed health response from a running sidecar.
type sidecarHealth struct {
	Status      string `json:"status"`
	NetworkMode string `json:"network_mode"`
}

// checkSidecar checks if a sidecar proxy is already listening on port 9119
// inside the given container. Returns nil if not running. If running, returns
// its current health state including network_mode.
func checkSidecar(ctx context.Context, ctr provider.ContainerProvider, containerID string) *sidecarHealth {
	if ctr == nil {
		return nil
	}
	result, err := ctr.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "curl -sf http://127.0.0.1:9119/health 2>/dev/null || wget -q -O - http://127.0.0.1:9119/health 2>/dev/null"},
		User:        "1002:1002",
	})
	if err != nil {
		return nil
	}
	output, _ := io.ReadAll(result.Reader)
	result.Reader.Close()
	var h sidecarHealth
	if err := json.Unmarshal(output, &h); err != nil {
		return nil
	}
	if h.Status != "ok" {
		return nil
	}
	return &h
}

// startSidecar launches the crewship-sidecar proxy inside the container.
// It pipes credentials via stdin JSON and waits for the "SIDECAR_READY" signal.
// The sidecar runs as a background process and intercepts all agent HTTP traffic.
// SidecarMemoryConfig is passed to the sidecar binary via stdin when memory is enabled.
type SidecarMemoryConfig struct {
	Enabled   bool   `json:"enabled"`
	BasePath  string `json:"base_path"`
	AgentSlug string `json:"agent_slug"`
}

// SidecarIPCConfig provides the crewshipd internal API address for the sidecar,
// allowing lead agents to forward assignment requests back to crewshipd.
// ContainerID is the Docker container ID where this agent is running; the sidecar
// forwards it to crewshipd so /keeper/execute can run commands in the right container.
type SidecarIPCConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	AgentID     string `json:"agent_id"`
	AgentSlug   string `json:"agent_slug"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
	ContainerID string `json:"container_id"`
}

// SidecarCrewMember describes a crew member accessible to lead agents for assignment.
type SidecarCrewMember struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RoleTitle string `json:"role_title"`
	ChatID    string `json:"chat_id,omitempty"`
}

// SidecarNetworkPolicy configures crew-level network access for the sidecar.
type SidecarNetworkPolicy struct {
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

func startSidecar(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	creds []Credential,
	memoryCfg *SidecarMemoryConfig,
	ipcCfg *SidecarIPCConfig,
	crewMembers []SidecarCrewMember,
	networkPolicy *SidecarNetworkPolicy,
	mcpServers []MCPServerConfig,
	logger *slog.Logger,
) error {
	type sidecarCred struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Token    string `json:"token"`
		Priority int    `json:"priority"`
	}

	var sc []sidecarCred
	for _, c := range creds {
		prov := credTypeToProvider(c)
		if prov == "" {
			continue
		}
		sc = append(sc, sidecarCred{
			ID:       c.ID,
			Provider: prov,
			Token:    c.PlainValue,
			Priority: c.Priority,
		})
	}
	if len(sc) == 0 {
		sc = []sidecarCred{}
	}

	// Build the input payload (new object format that includes memory config and IPC config)
	type sidecarMCPServer struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		DisplayName string            `json:"display_name"`
		Transport   string            `json:"transport"`
		Endpoint    string            `json:"endpoint,omitempty"`
		Command     string            `json:"command,omitempty"`
		Args        []string          `json:"args,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
		Credential  *MCPCredential    `json:"credential,omitempty"`
	}
	type sidecarInput struct {
		Credentials   []sidecarCred          `json:"credentials"`
		Memory        *SidecarMemoryConfig   `json:"memory,omitempty"`
		IPC           *SidecarIPCConfig      `json:"ipc,omitempty"`
		CrewMembers   []SidecarCrewMember    `json:"crew_members,omitempty"`
		NetworkPolicy *SidecarNetworkPolicy  `json:"network_policy,omitempty"`
		MCPServers    []sidecarMCPServer     `json:"mcp_servers,omitempty"`
	}

	// Only pass HTTP servers to sidecar — stdio servers are handled
	// by Claude Code directly via .mcp.json, not the gateway.
	var mcpInput []sidecarMCPServer
	for _, s := range mcpServers {
		if s.Transport != "streamable-http" {
			continue
		}
		mcpInput = append(mcpInput, sidecarMCPServer{
			ID: s.ID, Name: s.Name, DisplayName: s.DisplayName,
			Transport: s.Transport, Endpoint: s.Endpoint,
			Command: s.Command, Args: s.Args, Env: s.Env,
			Credential: s.Credential,
		})
	}

	input := sidecarInput{
		Credentials:   sc,
		Memory:        memoryCfg,
		IPC:           ipcCfg,
		CrewMembers:   crewMembers,
		NetworkPolicy: networkPolicy,
		MCPServers:    mcpInput,
	}

	credsJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal sidecar input: %w", err)
	}

	// SECURITY: Base64-encode the credentials JSON to prevent shell injection.
	// Raw JSON piped through `echo '...'` is vulnerable to shell metacharacter
	// injection if a credential token contains single quotes or other shell chars.
	credsB64 := base64.StdEncoding.EncodeToString(credsJSON)

	// Start sidecar as a background process.
	// Pipe credentials JSON via base64-decoded stdin to avoid shell injection.
	// Redirect stdout/stderr to files so the sidecar survives after Docker exec
	// stream closes (writes to closed pipes cause SIGPIPE which kills the process).
	// Health check: verify sidecar is responding, exit 1 on failure so orchestrator knows.
	script := fmt.Sprintf(
		`echo '%s' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 >/dev/null 2>>/tmp/sidecar.log &`+
			"\n"+`sleep 0.5`+"\n"+
			`if wget -q -O /dev/null http://127.0.0.1:9119/health 2>/dev/null; then exit 0; `+
			`elif curl -sf http://127.0.0.1:9119/health >/dev/null 2>&1; then exit 0; `+
			`else echo "sidecar health check failed" >&2; exit 1; fi`,
		credsB64,
	)

	// SECURITY: Run sidecar as UID 1002 (not 1001) so the agent process
	// cannot read /proc/<sidecar_pid>/mem to extract credentials from heap.
	// Linux kernel restricts /proc/PID/mem access to same-UID processes.
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1002:1002",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}

	output, readErr := io.ReadAll(result.Reader)
	result.Reader.Close()

	// Check if the health check script exited with an error
	running, exitCode, inspErr := container.ExecInspect(ctx, result.ExecID)
	if inspErr != nil {
		return fmt.Errorf("inspect sidecar exec: %w", inspErr)
	}
	if !running && exitCode != 0 {
		msg := strings.TrimSpace(string(output))
		if readErr != nil {
			msg += fmt.Sprintf(" (read error: %v)", readErr)
		}
		return fmt.Errorf("sidecar health check failed (exit %d): %s", exitCode, msg)
	}

	logger.Info("sidecar started",
		"container_id", containerID[:min(12, len(containerID))],
		"credentials", len(sc),
		"output_bytes", len(output),
	)
	return nil
}

// credTypeToProvider maps orchestrator credential types to sidecar provider types.
// AI_CLI_TOKEN (OAuth) returns "" — these are injected directly as CLAUDE_CODE_OAUTH_TOKEN
// env var in BuildEnvVarsSidecar rather than stored in the sidecar CredStore, because
// the sidecar CredStore only supports x-api-key injection which won't work for OAuth tokens.
func credTypeToProvider(c Credential) string {
	switch {
	case c.EnvVarName == "ANTHROPIC_API_KEY":
		return "ANTHROPIC"
	case c.EnvVarName == "OPENAI_API_KEY":
		return "OPENAI"
	case c.EnvVarName == "GOOGLE_API_KEY":
		return "GOOGLE"
	default:
		return ""
	}
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

// sanitizeMCPName restricts a server or package name to a safe basename,
// preventing path traversal and shell metacharacter injection.
func sanitizeMCPName(name string) string {
	// Take only the last path component.
	name = path.Base(name)
	// Remove any characters that aren't alphanumeric, dash, underscore, dot, or @.
	safe := regexp.MustCompile(`[^a-zA-Z0-9._@-]`).ReplaceAllString(name, "")
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

// streamJSONMessage represents a line from Claude Code --output-format stream-json.
// The format varies: top-level messages have "type" like "assistant", "result", "system";
// stream events have type "stream_event" with nested "event" containing deltas.
type streamJSONMessage struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`
	// For "assistant" type messages with content blocks at top level (legacy)
	Content []contentBlock `json:"content,omitempty"`
	// For "result" type
	Result       string          `json:"result,omitempty"`
	DurationMs   float64         `json:"duration_ms,omitempty"`
	DurationAPI  float64         `json:"duration_api_ms,omitempty"`
	TotalCostUSD float64         `json:"total_cost_usd,omitempty"`
	NumTurns     int             `json:"num_turns,omitempty"`
	IsError      bool            `json:"is_error,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	ModelUsage   json.RawMessage `json:"modelUsage,omitempty"`
	Errors       []string        `json:"errors,omitempty"`
	// For "system" type with subtype "init"
	Model    string            `json:"model,omitempty"`
	Tools    []string          `json:"tools,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	MCPSrvrs json.RawMessage   `json:"mcp_servers,omitempty"`
	// For stream_event type (--include-partial-messages)
	Event *streamEvent `json:"event,omitempty"`
}

// nestedMessage extracts content blocks from the "message" field if present.
// Claude Code stream-json wraps assistant content in {"type":"assistant","message":{"content":[...]}}.
type nestedMessage struct {
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type      string        `json:"type"`
	Text      string        `json:"text,omitempty"`
	Thinking  string        `json:"thinking,omitempty"`
	Name      string        `json:"name,omitempty"`
	ID        string        `json:"id,omitempty"`
	Input     any           `json:"input,omitempty"`
	ToolUseID string        `json:"tool_use_id,omitempty"`
	Source    *imageSource  `json:"source,omitempty"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type streamEvent struct {
	Type  string      `json:"type"`
	Delta *eventDelta `json:"delta,omitempty"`
}

type eventDelta struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

func (o *Orchestrator) streamOutput(ctx context.Context, result *provider.ExecResult, req AgentRunRequest, handler EventHandler) {
	var closeOnce sync.Once
	closeReader := func() {
		closeOnce.Do(func() {
			result.Reader.Close()
		})
	}
	defer closeReader()

	go func() {
		<-ctx.Done()
		closeReader()
	}()

	scanner := bufio.NewScanner(result.Reader)
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	useStreamJSON := req.CLIAdapter == "CLAUDE_CODE"

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		if useStreamJSON {
			o.handleStreamJSONLine(line, handler)
		} else {
			if handler != nil {
				handler(AgentEvent{
					Type:      "text",
					Content:   line + "\n",
					Timestamp: time.Now(),
				})
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}
}

// emitToolResultBlock sends a tool_result event for the given content block.
func emitToolResultBlock(block contentBlock, handler EventHandler) {
	meta := map[string]interface{}{}
	if block.ToolUseID != "" {
		meta["tool_use_id"] = block.ToolUseID
	}
	handler(AgentEvent{
		Type:      "tool_result",
		Content:   block.Text,
		Metadata:  meta,
		Timestamp: time.Now(),
	})
}

// emitImageBlock sends an image event for the given content block.
func emitImageBlock(block contentBlock, handler EventHandler) {
	if block.Source != nil && block.Source.Data != "" {
		handler(AgentEvent{
			Type:    "image",
			Content: block.Source.Data,
			Metadata: map[string]interface{}{
				"media_type": block.Source.MediaType,
			},
			Timestamp: time.Now(),
		})
	}
}

func (o *Orchestrator) handleStreamJSONLine(line string, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg streamJSONMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		// Not valid JSON -- emit as plain text (fallback)
		handler(AgentEvent{Type: "text", Content: line + "\n", Timestamp: time.Now()})
		return
	}

	// Claude Code wraps content in message.content; extract if top-level content is empty
	if len(msg.Content) == 0 && len(msg.Message) > 0 {
		var nested nestedMessage
		if json.Unmarshal(msg.Message, &nested) == nil && len(nested.Content) > 0 {
			msg.Content = nested.Content
		}
	}

	switch msg.Type {
	case "stream_event":
		// Token-level streaming (when --include-partial-messages is used)
		if msg.Event != nil && msg.Event.Delta != nil {
			switch msg.Event.Delta.Type {
			case "text_delta":
				handler(AgentEvent{Type: "text", Content: msg.Event.Delta.Text, Timestamp: time.Now()})
			case "thinking_delta":
				handler(AgentEvent{
					Type:      "thinking",
					Content:   msg.Event.Delta.Thinking,
					Metadata:  map[string]interface{}{"streaming": true},
					Timestamp: time.Now(),
				})
			}
		}

	case "assistant":
		// Complete assistant message with content blocks.
		// When --include-partial-messages is active (always for Claude Code),
		// text and thinking were already streamed via stream_event deltas.
		// We only emit tool_use and tool_result blocks here to avoid duplication.
		for _, block := range msg.Content {
			switch block.Type {
			case "thinking":
				// Already delivered via thinking_delta stream events — skip.
			case "text":
				// Already delivered via text_delta stream events — skip.
			case "tool_use":
				name := block.Name
				if name == "" {
					name = "tool"
				}
				handler(AgentEvent{
					Type:    "tool_call",
					Content: name,
					Metadata: map[string]interface{}{
						"tool_name": name,
						"tool_id":   block.ID,
						"input":     block.Input,
					},
					Timestamp: time.Now(),
				})
			case "tool_result":
				emitToolResultBlock(block, handler)
			case "image":
				emitImageBlock(block, handler)
			}
		}

	case "tool", "user":
		// Claude Code emits tool results as "tool" or "user" type messages
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_result":
				emitToolResultBlock(block, handler)
			case "image":
				emitImageBlock(block, handler)
			}
		}

	case "result":
		// Emit run result metadata (cost, usage, duration) as a dedicated event.
		// The text is NOT re-emitted (already delivered via "assistant" blocks).
		meta := map[string]interface{}{
			"subtype":        msg.Subtype,
			"duration_ms":    msg.DurationMs,
			"duration_api_ms": msg.DurationAPI,
			"total_cost_usd": msg.TotalCostUSD,
			"num_turns":      msg.NumTurns,
			"is_error":       msg.IsError,
		}
		if len(msg.Usage) > 0 {
			var usage map[string]interface{}
			if json.Unmarshal(msg.Usage, &usage) == nil {
				meta["usage"] = usage
			}
		}
		if len(msg.ModelUsage) > 0 {
			var mu map[string]interface{}
			if json.Unmarshal(msg.ModelUsage, &mu) == nil {
				meta["model_usage"] = mu
			}
		}
		if len(msg.Errors) > 0 {
			meta["errors"] = msg.Errors
		}
		handler(AgentEvent{
			Type:      "result",
			Content:   msg.Result,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	case "system":
		// Session init or compact boundary events
		meta := map[string]interface{}{
			"subtype": msg.Subtype,
		}
		if msg.Subtype == "init" {
			if msg.Model != "" {
				meta["model"] = msg.Model
			}
			if len(msg.Tools) > 0 {
				meta["tools"] = msg.Tools
			}
			if msg.CWD != "" {
				meta["cwd"] = msg.CWD
			}
			if len(msg.MCPSrvrs) > 0 {
				var servers []json.RawMessage
				if json.Unmarshal(msg.MCPSrvrs, &servers) == nil {
					meta["mcp_servers"] = servers
				}
			}
		}
		handler(AgentEvent{
			Type:      "system",
			Content:   msg.Subtype,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	default:
		// Unknown type -- emit raw content if any text content blocks exist
		for _, block := range msg.Content {
			if block.Text != "" {
				handler(AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()})
			}
		}
	}
}
