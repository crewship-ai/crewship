package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// claudeCodeAdapter wires Anthropic's `claude` CLI. Production-tested; this
// adapter must remain bit-for-bit compatible with the pre-refactor command
// shape because long-running missions and replay tests pin against it.
type claudeCodeAdapter struct{}

func (claudeCodeAdapter) Name() string { return "CLAUDE_CODE" }

func (claudeCodeAdapter) BuildCommand(req AgentRunRequest) []string {
	// --bare: skips auto-discovery of hooks/skills/plugins/MCP/CLAUDE.md.
	// Anthropic docs explicitly recommend it for scripted/SDK calls and say
	// it will become the default for -p in a future release. Crewship runs
	// in a clean container so discovery is pure overhead — and a stray
	// ~/.claude mount could silently inject behaviour. Combined with
	// --setting-sources (no value) and --strict-mcp-config we get clean
	// belt-and-suspenders isolation.
	cmd := []string{
		"claude", "--print",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
		"--verbose",
		"--bare",
		"--strict-mcp-config",
		"--no-session-persistence",
	}
	if req.LLMModel != "" {
		cmd = append(cmd, "--model", req.LLMModel)
	}
	systemPrompt := crewshipSystemPreamble + req.SystemPrompt
	cmd = append(cmd, "--system-prompt", systemPrompt)
	if req.ToolProfile == "MINIMAL" {
		cmd = append(cmd, "--tools", "Read,Search,Grep")
	}
	// --max-turns caps runaway loops at the Claude side as defense-in-depth
	// alongside Crewship's mission-level paymaster budget. 50 is generous
	// enough for complex multi-step tasks without letting a stuck agent
	// burn budget indefinitely.
	cmd = append(cmd, "--max-turns", "50")
	// MCP servers are read from /crew/agents/<slug>/.mcp.json — written by
	// WriteMCPConfig before exec when any MCP source is non-empty.
	if len(req.MCPServers) > 0 || req.CrewMCPConfigJSON != "" || req.AgentMCPConfigJSON != "" {
		cmd = append(cmd, "--mcp-config", fmt.Sprintf("/crew/agents/%s/.mcp.json", req.AgentSlug))
	}
	// `--` separator stops Claude Code from re-parsing user message tokens
	// that happen to start with `-` as flags.
	cmd = append(cmd, "--", req.UserMessage)
	return cmd
}

func (claudeCodeAdapter) UseStreamJSON() bool { return true }

func (claudeCodeAdapter) ParseStreamLine(line []byte, handler EventHandler) {
	parseClaudeCodeStreamJSON(line, handler)
}

// SetupSystemPrompt is a no-op for Claude Code: the system prompt is passed
// via --system-prompt, not via a file in the container.
func (claudeCodeAdapter) SetupSystemPrompt(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return nil
}

func (claudeCodeAdapter) SupportsMCP() bool { return true }

func (claudeCodeAdapter) WriteMCPConfig(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	workDir string,
	logger *slog.Logger,
) error {
	return writeMCPClaude(ctx, container, containerID, req, workDir, logger)
}

// parseClaudeCodeStreamJSON parses one line of Claude Code stream-json output
// and emits zero-or-more AgentEvents. Extracted from Orchestrator.handleStreamJSONLine
// so the adapter is stateless and easy to unit-test without a full Orchestrator.
func parseClaudeCodeStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg streamJSONMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	// Claude Code wraps content in message.content; promote it when top-level is empty.
	if len(msg.Content) == 0 && len(msg.Message) > 0 {
		var nested nestedMessage
		if json.Unmarshal(msg.Message, &nested) == nil && len(nested.Content) > 0 {
			msg.Content = nested.Content
		}
	}

	switch msg.Type {
	case "stream_event":
		// Token-level streaming (when --include-partial-messages is used).
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
		// With --include-partial-messages on (always), text and thinking
		// were streamed via stream_event already — only emit tool blocks
		// here so we don't duplicate the visible text.
		for _, block := range msg.Content {
			switch block.Type {
			case "thinking", "text":
				// Already delivered via deltas — skip.
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
		for _, block := range msg.Content {
			switch block.Type {
			case "tool_result":
				emitToolResultBlock(block, handler)
			case "image":
				emitImageBlock(block, handler)
			}
		}

	case "result":
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
		meta := map[string]interface{}{
			"subtype": msg.Subtype,
		}
		switch msg.Subtype {
		case "init":
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
			// v2.1.111+ ships plugins + plugin_errors so operators can see
			// when a plugin fails to load at session start. Crewship runs
			// --bare which suppresses plugin discovery, but customers who
			// later opt out of --bare on a per-agent basis benefit from
			// this visibility.
			if len(msg.Plugins) > 0 {
				var plugins json.RawMessage
				meta["plugins"] = json.RawMessage(append([]byte{}, msg.Plugins...))
				_ = plugins
			}
			if len(msg.PluginErrors) > 0 {
				meta["plugin_errors"] = json.RawMessage(append([]byte{}, msg.PluginErrors...))
			}
		case "api_retry":
			// Anthropic auth/rate/billing/server retry envelope. Capture all
			// fields so backoff investigations have ground truth; Crow's
			// Nest can render a "retrying" banner without polling logs.
			if msg.Attempt > 0 {
				meta["attempt"] = msg.Attempt
			}
			if msg.MaxRetries > 0 {
				meta["max_retries"] = msg.MaxRetries
			}
			if msg.RetryDelayMs > 0 {
				meta["retry_delay_ms"] = msg.RetryDelayMs
			}
			if msg.ErrorStatus > 0 {
				meta["error_status"] = msg.ErrorStatus
			}
			if msg.ErrorMessage != "" {
				meta["error"] = msg.ErrorMessage
			}
		}
		handler(AgentEvent{
			Type:      "system",
			Content:   msg.Subtype,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	default:
		for _, block := range msg.Content {
			if block.Text != "" {
				handler(AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()})
			}
		}
	}
}
