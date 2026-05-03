package orchestrator

import (
	"encoding/json"
	"time"
)

// parseGeminiStreamJSON parses one stdout line from `gemini -p X
// --output-format stream-json`. Schema documented at
// geminicli.com/docs/cli/headless — JSONL with these event types:
//
//   - init        — session bootstrap (model, version)
//   - message     — assistant text delta or full message
//   - tool_use    — tool invocation request
//   - tool_result — tool response
//   - error       — recoverable / fatal error from CLI
//   - result      — terminal envelope (response, stats, optional error)
//
// We coerce the gemini-specific fields into the same AgentEvent kinds the
// Claude Code parser emits so the chat UI doesn't fork per provider.
type geminiStreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	// init
	Model     string `json:"model,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// message
	Text    string `json:"text,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// tool_use
	ToolName string          `json:"name,omitempty"`
	ToolID   string          `json:"id,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Output    string `json:"output,omitempty"`
	// error
	Error string `json:"error,omitempty"`
	// result
	Response string          `json:"response,omitempty"`
	Stats    json.RawMessage `json:"stats,omitempty"`
}

func parseGeminiStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg geminiStreamMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "init":
		handler(AgentEvent{
			Type:    "system",
			Content: "init",
			Metadata: map[string]interface{}{
				"subtype":    "init",
				"model":      msg.Model,
				"session_id": msg.SessionID,
			},
			Timestamp: time.Now(),
		})

	case "message":
		// Gemini's "message" carries assistant text — sometimes a delta, sometimes
		// the full payload depending on streaming. Either text/content field may
		// hold it; favour text since it's the documented field for stream-json.
		body := msg.Text
		if body == "" {
			body = msg.Content
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "tool_use":
		var input any
		if len(msg.Input) > 0 {
			_ = json.Unmarshal(msg.Input, &input)
		}
		handler(AgentEvent{
			Type:    "tool_call",
			Content: msg.ToolName,
			Metadata: map[string]interface{}{
				"tool_name": msg.ToolName,
				"tool_id":   msg.ToolID,
				"input":     input,
			},
			Timestamp: time.Now(),
		})

	case "tool_result":
		handler(AgentEvent{
			Type:    "tool_result",
			Content: msg.Output,
			Metadata: map[string]interface{}{
				"tool_use_id": msg.ToolUseID,
			},
			Timestamp: time.Now(),
		})

	case "error":
		// Recoverable error — surface to chat as an error event.
		handler(AgentEvent{
			Type:      "error",
			Content:   msg.Error,
			Metadata:  map[string]interface{}{"subtype": msg.Subtype},
			Timestamp: time.Now(),
		})

	case "result":
		// Terminal envelope. Stats schema is documented as { totalTokens,
		// inputTokens, outputTokens, cachedInputTokens, ... } — we hand the
		// raw blob to Paymaster (downstream) without strongly typing it here
		// so future fields don't require parser updates.
		var stats map[string]interface{}
		if len(msg.Stats) > 0 {
			_ = json.Unmarshal(msg.Stats, &stats)
		}
		handler(AgentEvent{
			Type:    "result",
			Content: msg.Response,
			Metadata: map[string]interface{}{
				"stats":    stats,
				"is_error": msg.Error != "",
			},
			Timestamp: time.Now(),
		})

	default:
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
