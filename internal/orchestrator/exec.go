package orchestrator

import (
	"fmt"
	"regexp"
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

EXPOSE PORT (show a running server to the user):
- When you run a TCP server inside this container (HTTP, dev preview, etc.) the user
  cannot reach it directly because the container has no host port mapping.
- To get a public URL the user can paste into their browser, call the sidecar:
    curl -s -X POST http://localhost:9119/expose-port \
      -H "Content-Type: application/json" \
      -d '{"port": <port>, "description": "<short why>"}'
- Response: {"token": "...", "url": "http://<host>/exposed/<token>/", "expires_at": "..."}
- Share the "url" field with the user. It expires in 1 hour by default; pass
  "ttl_seconds": N to request a different TTL (max 24h). The URL is a capability
  — anyone with it reaches the server, so avoid posting it to public channels.
- Bind your server to 0.0.0.0 (not 127.0.0.1) so the reverse proxy can reach it.
`

// BuildCLICommand constructs the CLI command and arguments for the configured
// adapter. Supported adapters as of 2026-04:
//   - CLAUDE_CODE   — Anthropic's `claude` CLI (Max subscription or API key)
//   - CODEX_CLI     — OpenAI's `codex` (ChatGPT Plus/Pro or API key)
//   - GEMINI_CLI    — Google's `gemini` (Google AI Pro/Ultra or API key)
//   - OPENCODE      — sst.dev's `opencode` (BYOK any provider)
//   - CURSOR_CLI    — Cursor's `cursor-agent` headless mode (added 2026-04)
//   - FACTORY_DROID — Factory's `droid exec` autonomous runs (added 2026-04)
//
// Aider, Copilot CLI, Cody CLI, Replit Agent are intentionally NOT here:
// either too pair-programming-shaped, IDE-tied, browser-only, or shipping
// breaking changes too aggressively to integrate cleanly right now.
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

	case "CURSOR_CLI":
		// Cursor's headless agent. `-p` (print mode) prevents the interactive
		// TUI from spawning; `--output-format stream-json` aligns its JSONL
		// stream with what we already parse for Claude Code so the chat-bridge
		// doesn't need a Cursor-specific reader. `--mode=plan` and `--mode=ask`
		// are supported flags (added 2026-01-16) but we leave the default
		// "agent" mode here — adapters can extend this case if a tool profile
		// asks for read-only browsing.
		//
		// System instructions: Cursor reads `.cursor/rules/`, `AGENTS.md`, and
		// `CLAUDE.md` from the working directory. setupSystemPromptFiles()
		// writes those before exec, so no `--system-prompt` flag is needed.
		cmd := []string{"cursor-agent", "-p", "--output-format", "stream-json"}
		if req.LLMModel != "" {
			cmd = append(cmd, "-m", req.LLMModel)
		}
		// `--` separator is defensive: a Cursor flag landing in the message
		// body should not be re-parsed by the CLI.
		cmd = append(cmd, "--", req.UserMessage)
		return cmd

	case "FACTORY_DROID":
		// Factory's `droid exec` is the headless single-shot mode of Droid.
		// Tiered autonomy via --auto:
		//   low    — read-only (no file mutations)
		//   medium — can edit files within scope
		//   high   — fully autonomous (Factory's docs warn it can do
		//            destructive things without further confirmation,
		//            so we never opt in here; future profile if a
		//            customer needs it)
		//
		// Default policy: medium. CODING is the API default (see
		// internal/api/agents_create.go normalising empty ToolProfile to
		// "CODING") so almost every agent reaches BuildCLICommand with
		// CODING set, and these agents are expected to write code —
		// medium is the closest match. MINIMAL / CONSULTATIVE profiles
		// downgrade to low because those signals are explicit "this agent
		// should not mutate anything". This inversion (default-medium,
		// explicit-low) is honest about production behaviour where the
		// previous default-low was a comment that production never hit.
		autonomy := "medium"
		switch req.ToolProfile {
		case "MINIMAL", "CONSULTATIVE":
			autonomy = "low"
		}
		cmd := []string{"droid", "exec", "--auto", autonomy}
		if req.LLMModel != "" {
			cmd = append(cmd, "--model", req.LLMModel)
		}
		cmd = append(cmd, req.UserMessage)
		return cmd

	default:
		return []string{"claude", "--print", req.UserMessage}
	}
}

// BuildEnvVars constructs the environment variables for a container exec,
// including agent identity, credentials (when sidecar is not used), and
// provider-specific settings. nodeJSLauncher/filterNpxServers/buildMCPConfig
// and friends live in exec_mcp.go on this branch (file-split refactor).
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
