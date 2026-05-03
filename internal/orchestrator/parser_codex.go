package orchestrator

import (
	"encoding/json"
	"time"
)

// parseCodexStreamJSON parses one stdout line from `codex exec --json`.
// Reference: developers.openai.com/codex/cli/reference (the --json output
// section). The Rust port emits newline-delimited events with a "type"
// discriminator; the schema is loosely modelled on OpenAI's Realtime / Agents
// SDK event taxonomy.
//
// Documented event types:
//   - session.started        — session bootstrap (model, sandbox, cwd)
//   - agent.message          — assistant text / final answer
//   - agent.message.delta    — streaming text delta (when --json is paired
//     with streaming, which is the default)
//   - tool.call              — tool invocation
//   - tool.result            — tool response
//   - error                  — recoverable error from the CLI / model
//   - session.ended          — terminal envelope with usage + duration
//
// Some Codex builds prefix events with shorter aliases (e.g. "delta" for
// agent.message.delta); we accept both via the contains-check fallback path.
type codexStreamMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Model   string          `json:"model,omitempty"`
	Text    string          `json:"text,omitempty"`
	Delta   string          `json:"delta,omitempty"`
	Content string          `json:"content,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"arguments,omitempty"`
	Output  string          `json:"output,omitempty"`
	Error   string          `json:"error,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
}

func parseCodexStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg codexStreamMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "session.started", "session.start":
		handler(AgentEvent{
			Type:    "system",
			Content: "init",
			Metadata: map[string]interface{}{
				"subtype": "init",
				"model":   msg.Model,
			},
			Timestamp: time.Now(),
		})

	case "agent.message.delta", "delta", "message.delta":
		// Token-level streaming text. Content lives in Delta or Text depending
		// on Codex build; favour Delta since that's the documented field for
		// streaming events.
		body := msg.Delta
		if body == "" {
			body = msg.Text
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "agent.message", "message":
		// Full assistant message. With deltas already streamed the Text here
		// would duplicate, but some Codex builds emit ONLY this event without
		// deltas. We can't know which without a fixture, so we emit — chat UI
		// can dedupe if needed (and Crow's Nest journal sees both either way).
		body := msg.Text
		if body == "" {
			body = msg.Content
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "tool.call", "function.call":
		var input any
		if len(msg.Input) > 0 {
			_ = json.Unmarshal(msg.Input, &input)
		}
		handler(AgentEvent{
			Type:    "tool_call",
			Content: msg.Name,
			Metadata: map[string]interface{}{
				"tool_name": msg.Name,
				"tool_id":   msg.ID,
				"input":     input,
			},
			Timestamp: time.Now(),
		})

	case "tool.result", "function.result":
		handler(AgentEvent{
			Type:    "tool_result",
			Content: msg.Output,
			Metadata: map[string]interface{}{
				"tool_use_id": msg.ID,
			},
			Timestamp: time.Now(),
		})

	case "error":
		handler(AgentEvent{
			Type:      "error",
			Content:   msg.Error,
			Timestamp: time.Now(),
		})

	case "session.ended", "session.end", "result":
		// Terminal envelope. Usage shape mirrors OpenAI's chat completion
		// response: { input_tokens, output_tokens, total_tokens }.
		var usage map[string]interface{}
		if len(msg.Usage) > 0 {
			_ = json.Unmarshal(msg.Usage, &usage)
		}
		handler(AgentEvent{
			Type:    "result",
			Content: msg.Text,
			Metadata: map[string]interface{}{
				"usage":    usage,
				"is_error": msg.Error != "",
			},
			Timestamp: time.Now(),
		})

	default:
		// Unknown type — surface raw line for forward compat.
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
