package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

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
		// No user/crew MCP input AND no resolved bindings: start from an
		// empty document so the memory injection step below has something
		// to add to. Pre-PR-A this early-returned when !hadMCPInput, but
		// PR-A F1 needs every Claude run to land at least the
		// crewship-memory server so the model gets native memory.* tool
		// calls regardless of whether the operator wired any other MCP
		// servers — short-circuiting here would silently skip the
		// guaranteed-on memory surface.
		mcpJSON = `{"mcpServers":{}}`
		_ = hadMCPInput
	}

	// PR-A F1: auto-inject the sidecar-hosted memory MCP server into the
	// final Claude .mcp.json. Safe even when the operator declared
	// crewship-memory themselves — injectMemoryMCPIntoClaudeJSON is a
	// no-op in that case (the user entry wins, see injectMemoryMCP).
	if injected, err := injectMemoryMCPIntoClaudeJSON(mcpJSON); err == nil {
		mcpJSON = injected
	} else if logger != nil {
		// Don't fail the whole run — the model can still operate without
		// memory tools; just log so an operator inspecting startup sees the
		// degradation.
		logger.Warn("memory MCP injection failed; agent will have no memory tools", "error", err)
	}

	// Auto-inject the sidecar-hosted routine-authoring MCP server (save_routine
	// / list_routines) so the model authors routines as native tool calls
	// instead of shelling out to curl /pipelines/save. No-op if the operator
	// already declared a server named "crewship-routines".
	if injected, err := injectRoutinesMCPIntoClaudeJSON(mcpJSON); err == nil {
		mcpJSON = injected
	} else if logger != nil {
		logger.Warn("routines MCP injection failed; agent will have no save_routine tool", "error", err)
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

// setupSystemPromptFiles dispatches to the adapter's SetupSystemPrompt method.
// Each adapter knows whether it needs to drop AGENTS.md / .cursor/rules /
// CLAUDE.md into the working directory or whether it accepts the system prompt
// via a CLI flag (Claude Code, Gemini). Kept as a free function so the call
// site in orchestrator_run.go does not need to look up the adapter itself.
func setupSystemPromptFiles(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return getAdapter(req.CLIAdapter).SetupSystemPrompt(ctx, container, containerID, req, workDir, logger)
}
