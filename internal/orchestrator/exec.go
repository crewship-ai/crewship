package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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
// adapter (CLAUDE_CODE, OPENCODE, CODEX_CLI, or GEMINI_CLI).
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
	Model    string          `json:"model,omitempty"`
	Tools    []string        `json:"tools,omitempty"`
	CWD      string          `json:"cwd,omitempty"`
	MCPSrvrs json.RawMessage `json:"mcp_servers,omitempty"`
	// For stream_event type (--include-partial-messages)
	Event *streamEvent `json:"event,omitempty"`
}

// nestedMessage extracts content blocks from the "message" field if present.
// Claude Code stream-json wraps assistant content in {"type":"assistant","message":{"content":[...]}}.
type nestedMessage struct {
	Content []contentBlock `json:"content,omitempty"`
}

type contentBlock struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Name      string       `json:"name,omitempty"`
	ID        string       `json:"id,omitempty"`
	Input     any          `json:"input,omitempty"`
	ToolUseID string       `json:"tool_use_id,omitempty"`
	Source    *imageSource `json:"source,omitempty"`
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

	// Crow's Nest: capture the first 16 KB of raw stdout+stderr so the live
	// terminal panel can show a replayable snapshot. We deliberately do NOT
	// emit per-line — at 50 lines/sec that would flood the journal. The live
	// WebSocket stream (handler → wsHub) already carries real-time output to
	// the UI; this journal entry is the persistence + replay layer, so a
	// single end-of-stream summary is the right grain. totalBytes records
	// the full byte count (un-truncated) so consumers know how much was
	// dropped from the snapshot.
	const captureCap = 16 * 1024 // 16 KB cap for in-memory buffer
	captureBuf := make([]byte, 0, captureCap)
	var totalBytes int64

	for scanner.Scan() {
		// scanner.Bytes() aliases the scanner's internal buffer, so it's
		// valid only until the next Scan(). handleStreamJSONLine consumes
		// the slice synchronously (json.Unmarshal copies strings out), and
		// the non-JSON fallback below converts to string immediately.
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Accumulate for the end-of-stream journal emit. Once the buffer is
		// full we still count totalBytes so the summary reflects the real
		// volume even though the sample is capped.
		totalBytes += int64(len(line)) + 1 // +1 for the newline the scanner strips
		if len(captureBuf) < captureCap {
			remaining := captureCap - len(captureBuf)
			if len(line) <= remaining {
				captureBuf = append(captureBuf, line...)
				if len(captureBuf) < captureCap {
					captureBuf = append(captureBuf, '\n')
				}
			} else {
				captureBuf = append(captureBuf, line[:remaining]...)
			}
		}

		if useStreamJSON {
			o.handleStreamJSONLine(line, handler)
		} else {
			if handler != nil {
				handler(AgentEvent{
					Type:      "text",
					Content:   string(line) + "\n",
					Timestamp: time.Now(),
				})
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		o.logger.Debug("scanner error", "error", err, "agent_id", req.AgentID)
	}

	// End-of-stream Crow's Nest emit. We run unconditionally (even when
	// totalBytes is 0) because an empty-output run is still interesting for
	// debugging — the UI can render "agent produced no stdout" explicitly
	// instead of showing a hanging block. Use a fresh context in case the
	// request context was cancelled (user pressed stop); we still want the
	// capture recorded for post-mortem. ExecID lives on the provider
	// ExecResult so we record it in the payload for correlation with the
	// exec.command end event.
	emitCtx := ctx
	if emitCtx.Err() != nil {
		emitCtx = context.Background()
	}
	payload := map[string]any{
		"output":      string(captureBuf),
		"total_bytes": totalBytes,
		"truncated":   totalBytes > int64(len(captureBuf)),
	}
	if result != nil && result.ExecID != "" {
		payload["exec_id"] = result.ExecID
	}
	_, _ = o.getJournal().Emit(emitCtx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        "exec.output_chunk",
		Severity:    "info",
		ActorType:   "sidecar",
		ActorID:     req.AgentID,
		Summary:     fmt.Sprintf("%s stdout+stderr capture (%d bytes)", req.AgentSlug, totalBytes),
		Payload:     payload,
		Refs:        map[string]any{"chat_id": req.ChatID},
	})
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

func (o *Orchestrator) handleStreamJSONLine(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg streamJSONMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		// Not valid JSON -- emit as plain text (fallback). Convert to string
		// here so we don't alias the scanner buffer after this return.
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
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
			"subtype":         msg.Subtype,
			"duration_ms":     msg.DurationMs,
			"duration_api_ms": msg.DurationAPI,
			"total_cost_usd":  msg.TotalCostUSD,
			"num_turns":       msg.NumTurns,
			"is_error":        msg.IsError,
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
