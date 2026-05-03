package orchestrator

import (
	"encoding/json"
	"time"
)

// parseDroidStreamJSON parses one stdout line from `droid exec -o stream-json`.
// Schema verified against docs.factory.ai/cli/droid-exec/overview (May 2026).
//
// Top-level event types (discriminator: "type"):
//
//   - system     subtype "init" — { cwd, session_id, tools, model }
//   - message    { role, id, text, timestamp, session_id }
//     role is assistant for chat output
//   - tool_call  { id, messageId, toolId, toolName, parameters,
//     timestamp, session_id }
//     NOTE: toolName + parameters are camelCase
//   - tool_result { id, messageId, toolId, isError, value, timestamp,
//     session_id }
//     NOTE: value (not output), isError (not is_error)
//   - completion { finalText, numTurns, durationMs, session_id, timestamp }
//     NOTE: snake_case "completion" event uses camelCase fields
//   - result     { subtype, is_error, duration_ms, num_turns, result,
//     session_id }
//     NOTE: snake_case (different from completion above)
//   - error      generic error envelope
//
// Field-name camelCase / snake_case is inconsistent across events — Droid
// emits each event in the convention of its underlying source code, so the
// parser must accept both flavours per event type.
type droidStreamMessage struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	// system init
	CWD   string          `json:"cwd,omitempty"`
	Tools json.RawMessage `json:"tools,omitempty"`
	Model string          `json:"model,omitempty"`

	// message
	Role string `json:"role,omitempty"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`

	// tool_call (camelCase!)
	MessageID  string          `json:"messageId,omitempty"`
	ToolID     string          `json:"toolId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`

	// tool_result (camelCase isError, value)
	IsError bool   `json:"isError,omitempty"`
	Value   string `json:"value,omitempty"`

	// completion (camelCase)
	FinalText  string  `json:"finalText,omitempty"`
	NumTurns   int     `json:"numTurns,omitempty"`
	DurationMs float64 `json:"durationMs,omitempty"`

	// result (snake_case — different convention from completion!)
	IsErrorSnake  bool    `json:"is_error,omitempty"`
	DurationMsSnk float64 `json:"duration_ms,omitempty"`
	NumTurnsSnake int     `json:"num_turns,omitempty"`
	ResultText    string  `json:"result,omitempty"`

	// error
	Error string `json:"error,omitempty"`
}

func parseDroidStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg droidStreamMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "system":
		var tools any
		if len(msg.Tools) > 0 {
			_ = json.Unmarshal(msg.Tools, &tools)
		}
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Subtype,
			Metadata: map[string]interface{}{
				"subtype":    msg.Subtype,
				"cwd":        msg.CWD,
				"session_id": msg.SessionID,
				"model":      msg.Model,
				"tools":      tools,
			},
			Timestamp: time.Now(),
		})

	case "message":
		// Assistant text. Droid does not stream deltas in stream-json mode
		// (per current docs); each message event is one full chunk.
		if msg.Text != "" {
			handler(AgentEvent{Type: "text", Content: msg.Text, Timestamp: time.Now()})
		}

	case "tool_call":
		// camelCase: toolName, parameters. Pre-fix parser read name + input
		// and silently produced empty tool_call events.
		var input any
		if len(msg.Parameters) > 0 {
			_ = json.Unmarshal(msg.Parameters, &input)
		}
		handler(AgentEvent{
			Type:    "tool_call",
			Content: msg.ToolName,
			Metadata: map[string]interface{}{
				"tool_name":  msg.ToolName,
				"tool_id":    msg.ToolID,
				"input":      input,
				"message_id": msg.MessageID,
			},
			Timestamp: time.Now(),
		})

	case "tool_result":
		// camelCase: value (NOT output). Pre-fix parser silently dropped tool
		// outputs because it read item.Output.
		handler(AgentEvent{
			Type:    "tool_result",
			Content: msg.Value,
			Metadata: map[string]interface{}{
				"tool_use_id": msg.ToolID,
				"is_error":    msg.IsError,
				"message_id":  msg.MessageID,
			},
			Timestamp: time.Now(),
		})

	case "completion":
		// Droid's per-turn completion envelope. camelCase fields. Surface as
		// result so Paymaster + chat-bridge see it.
		handler(AgentEvent{
			Type:    "result",
			Content: msg.FinalText,
			Metadata: map[string]interface{}{
				"subtype":     "completion",
				"num_turns":   msg.NumTurns,
				"duration_ms": msg.DurationMs,
				"is_error":    false,
			},
			Timestamp: time.Now(),
		})

	case "result":
		// Snake_case-flavoured result event (different convention from
		// completion above — Droid is internally inconsistent). Carries
		// is_error, duration_ms, num_turns, result.
		handler(AgentEvent{
			Type:    "result",
			Content: msg.ResultText,
			Metadata: map[string]interface{}{
				"subtype":     msg.Subtype,
				"num_turns":   msg.NumTurnsSnake,
				"duration_ms": msg.DurationMsSnk,
				"is_error":    msg.IsErrorSnake,
			},
			Timestamp: time.Now(),
		})

	case "error":
		handler(AgentEvent{
			Type:      "error",
			Content:   msg.Error,
			Timestamp: time.Now(),
		})

	default:
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
