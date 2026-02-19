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
Scratch space is available at /workspace/ for temporary files that don't need to be visible.
Do NOT attempt to write outside /workspace or /output -- the filesystem is read-only elsewhere.
`

func BuildCLICommand(req AgentRunRequest) []string {
	switch req.CLIAdapter {
	case "CLAUDE_CODE":
		cmd := []string{
			"claude", "--print",
			"--output-format", "stream-json",
			"--dangerously-skip-permissions",
			"--verbose",
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
		"HOME=/home/agent",
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
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
// Credentials are NOT included -- the sidecar proxy injects them into HTTP requests.
// The agent gets dummy/empty API keys and proxy configuration pointing to the sidecar.
func BuildEnvVarsSidecar(req AgentRunRequest) []string {
	env := []string{
		"HOME=/home/agent",
		"CLAUDE_CODE_DISABLE_AUTOUPDATE=1",
		"CREWSHIP_AGENT_ID=" + req.AgentID,
		"CREWSHIP_CREW_ID=" + req.CrewID,
		"CREWSHIP_CHAT_ID=" + req.ChatID,
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
		// Claude Code: point base URL to sidecar so it uses HTTP (not HTTPS)
		// and the proxy can inject the real API key
		"ANTHROPIC_BASE_URL=http://127.0.0.1:9119",
		// Dummy keys -- the sidecar replaces them with real ones per-request
		"ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar",
		"OPENAI_API_KEY=sk-dummy-crewship-sidecar",
		"GOOGLE_API_KEY=dummy-crewship-sidecar",
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

// startSidecar launches the crewship-sidecar proxy inside the container.
// It pipes credentials via stdin JSON and waits for the "SIDECAR_READY" signal.
// The sidecar runs as a background process and intercepts all agent HTTP traffic.
func startSidecar(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	creds []Credential,
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

	credsJSON, err := json.Marshal(sc)
	if err != nil {
		return fmt.Errorf("marshal sidecar credentials: %w", err)
	}

	// SECURITY: Base64-encode the credentials JSON to prevent shell injection.
	// Raw JSON piped through `echo '...'` is vulnerable to shell metacharacter
	// injection if a credential token contains single quotes or other shell chars.
	credsB64 := base64.StdEncoding.EncodeToString(credsJSON)

	// Start sidecar as a background process.
	// Pipe credentials JSON via base64-decoded stdin to avoid shell injection.
	// Health check: verify sidecar is responding, exit 1 on failure so orchestrator knows.
	script := fmt.Sprintf(
		`echo '%s' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 &`+
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
func credTypeToProvider(c Credential) string {
	switch {
	case c.EnvVarName == "ANTHROPIC_API_KEY" || c.Type == "AI_CLI_TOKEN":
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
	logger *slog.Logger,
) error {
	script := `mkdir -p /home/agent/.claude && ` +
		`cat > /home/agent/.claude.json << 'CFGEOF'
{"hasCompletedOnboarding":true,"hasAvailableSubscription":true,"autoUpdates":false}
CFGEOF
chmod 600 /home/agent/.claude.json`

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
	Message json.RawMessage `json:"message,omitempty"`
	// For "assistant" type messages with content blocks
	Content []contentBlock `json:"content,omitempty"`
	// For "result" type
	Result string `json:"result,omitempty"`
	// For stream_event type (--include-partial-messages)
	Event *streamEvent `json:"event,omitempty"`
}

type contentBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

type streamEvent struct {
	Type  string      `json:"type"`
	Delta *eventDelta `json:"delta,omitempty"`
}

type eventDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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

	switch msg.Type {
	case "stream_event":
		// Token-level streaming (when --include-partial-messages is used)
		if msg.Event != nil && msg.Event.Delta != nil && msg.Event.Delta.Type == "text_delta" {
			handler(AgentEvent{Type: "text", Content: msg.Event.Delta.Text, Timestamp: time.Now()})
		}

	case "assistant":
		// Complete assistant message with content blocks
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				handler(AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()})
			case "tool_use":
				name := block.Name
				if name == "" {
					name = "tool"
				}
				handler(AgentEvent{
					Type:      "tool_call",
					Content:   fmt.Sprintf("Using tool: %s", name),
					Metadata:  block.Input,
					Timestamp: time.Now(),
				})
			case "tool_result":
				handler(AgentEvent{Type: "tool_result", Content: block.Text, Timestamp: time.Now()})
			}
		}

	case "result":
		if msg.Result != "" {
			handler(AgentEvent{Type: "text", Content: msg.Result, Timestamp: time.Now()})
		}

	default:
		// Unknown type -- emit raw content if any text content blocks exist
		for _, block := range msg.Content {
			if block.Text != "" {
				handler(AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()})
			}
		}
	}
}
