package orchestrator

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const crewshipSystemPreamble = `You are running inside a Crewship agent container.
Your working directory IS the output directory -- files you create or edit here are immediately visible to the user in the Files panel.

FILESYSTEM:
- HOME (~/) = /crew/agents/{your-slug}/ — persistent, personal (config, memory)
- Working dir = /output/{your-slug}/ — visible in Files panel
- Shared crew space = /crew/shared/ — all crew members can read/write
- Scratch = /workspace/ — temporary, not persistent
Do NOT attempt to write outside these directories -- the filesystem is read-only elsewhere.
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
// AI_CLI_TOKEN (OAuth setup tokens) use CLAUDE_CODE_OAUTH_TOKEN for Claude Code.
func resolveEnvVar(cred *Credential) string {
	if cred.Type == "AI_CLI_TOKEN" {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return cred.EnvVarName
}

// isSidecarRunning checks if a sidecar proxy is already listening on port 9119
// inside the given container. Used to avoid port conflicts when multiple agents
// in the same crew container start concurrently.
func isSidecarRunning(ctx context.Context, ctr provider.ContainerProvider, containerID string) bool {
	if ctr == nil {
		return false
	}
	result, err := ctr.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "curl -sf http://127.0.0.1:9119/healthz 2>/dev/null || wget -q -O - http://127.0.0.1:9119/healthz 2>/dev/null"},
		User:        "1002:1002",
	})
	if err != nil {
		return false
	}
	output, _ := io.ReadAll(result.Reader)
	result.Reader.Close()
	return strings.Contains(string(output), "ok")
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

func startSidecar(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	creds []Credential,
	memoryCfg *SidecarMemoryConfig,
	ipcCfg *SidecarIPCConfig,
	crewMembers []SidecarCrewMember,
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
	type sidecarInput struct {
		Credentials []sidecarCred       `json:"credentials"`
		Memory      *SidecarMemoryConfig `json:"memory,omitempty"`
		IPC         *SidecarIPCConfig    `json:"ipc,omitempty"`
		CrewMembers []SidecarCrewMember  `json:"crew_members,omitempty"`
	}
	input := sidecarInput{
		Credentials: sc,
		Memory:      memoryCfg,
		IPC:         ipcCfg,
		CrewMembers: crewMembers,
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
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

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
			case "image":
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
		}

	case "tool", "user":
		// Claude Code emits tool results as "tool" or "user" type messages
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_result":
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
			case "image":
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
				var servers []interface{}
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
