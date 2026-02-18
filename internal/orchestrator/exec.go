package orchestrator

import (
	"bufio"
	"context"
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

// resolveEnvVar returns the correct env var name for a credential.
// AI_CLI_TOKEN (OAuth setup tokens) use CLAUDE_CODE_OAUTH_TOKEN for Claude Code.
func resolveEnvVar(cred *Credential) string {
	if cred.Type == "AI_CLI_TOKEN" {
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
	return cred.EnvVarName
}

// credentialsJSON is the format Claude CLI expects at ~/.claude/.credentials.json
type credentialsJSON struct {
	ClaudeAiOauth oauthEntry `json:"claudeAiOauth"`
}

type oauthEntry struct {
	AccessToken  string   `json:"accessToken"`
	RefreshToken string   `json:"refreshToken"`
	ExpiresAt    string   `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
}

// claudeConfigJSON is ~/.claude.json -- skips onboarding in the container.
type claudeConfigJSON struct {
	HasCompletedOnboarding   bool `json:"hasCompletedOnboarding"`
	HasAvailableSubscription bool `json:"hasAvailableSubscription"`
	AutoUpdates              bool `json:"autoUpdates"`
}

// setupClaudeCredentials writes OAuth credential files into the agent container
// so that `claude --print` can authenticate with a Pro/Max subscription token.
// This follows the pattern from cabinlab/claude-code-sdk-docker.
func setupClaudeCredentials(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	cred *Credential,
	logger *slog.Logger,
) error {
	if cred == nil || cred.PlainValue == "" {
		return nil
	}
	if cred.Type != "AI_CLI_TOKEN" {
		return nil // only inject files for OAuth setup tokens
	}

	token := cred.PlainValue

	credsData, err := json.Marshal(credentialsJSON{
		ClaudeAiOauth: oauthEntry{
			AccessToken:  token,
			RefreshToken: token,
			ExpiresAt:    "2099-12-31T23:59:59.999Z",
			Scopes:       []string{"user:inference", "user:profile"},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal credentials.json: %w", err)
	}

	configData, err := json.Marshal(claudeConfigJSON{
		HasCompletedOnboarding:   true,
		HasAvailableSubscription: true,
		AutoUpdates:              false,
	})
	if err != nil {
		return fmt.Errorf("marshal claude.json: %w", err)
	}

	// Write credentials file and patch .claude.json (merge if exists, create if not).
	// Uses jq if available for merging, falls back to overwrite.
	script := fmt.Sprintf(
		`mkdir -p /home/agent/.claude && `+
			`cat > /home/agent/.claude/.credentials.json << 'CREDEOF'
%s
CREDEOF
if command -v jq >/dev/null 2>&1 && [ -f /home/agent/.claude.json ]; then
  jq '. + {"hasCompletedOnboarding":true,"hasAvailableSubscription":true,"autoUpdates":false}' /home/agent/.claude.json > /tmp/.claude.json.tmp && mv /tmp/.claude.json.tmp /home/agent/.claude.json
else
  cat > /home/agent/.claude.json << 'CFGEOF'
%s
CFGEOF
fi
chmod 600 /home/agent/.claude/.credentials.json /home/agent/.claude.json`,
		strings.ReplaceAll(string(credsData), "'", "'\\''"),
		strings.ReplaceAll(string(configData), "'", "'\\''"),
	)

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write claude credentials: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Debug("claude credentials injected", "container_id", containerID[:min(12, len(containerID))])
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
	if systemPrompt == "" {
		return nil
	}

	var script string

	switch req.CLIAdapter {
	case "OPENCODE":
		// OpenCode reads AGENTS.md from the project root / CWD for instructions.
		escaped := strings.ReplaceAll(systemPrompt, "'", "'\\''")
		script = fmt.Sprintf("cat > %s/AGENTS.md << 'PROMPTEOF'\n%s\nPROMPTEOF", workDir, escaped)

	default:
		return nil
	}

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
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
