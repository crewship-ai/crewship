package orchestrator

import (
	"encoding/json"
	"time"
)

// parseOpenCodeStreamJSON parses one stdout line from `opencode run --format
// json`. Schema verified against the active upstream
// github.com/anomalyco/opencode (NOTE: sst/opencode no longer exists; the
// pre-2026 opencode-ai/opencode is archived). The current emitter is
// packages/opencode/src/cli/cmd/run.ts which writes JSON.stringify({
// type, timestamp, sessionID, ...data }) — a FLAT envelope, not a
// nested {part: {type: ...}} shape.
//
// Top-level "type" values seen in the wild:
//
//   - text          — assistant text chunk;       data has `part`
//   - reasoning     — chain-of-thought;           data has `part`
//   - tool_use      — tool invocation;            data has `part` (with state)
//   - step_start    — model turn boundary begin;  data has `part`
//   - step_finish   — model turn boundary end +   data has `part` (tokens, cost)
//     usage / cost
//   - error         — fatal error;                data has `error` string
//
// IDs: messageID and partID live INSIDE part (part.id), not at envelope
// level. The envelope only carries sessionID.
type opencodeEnvelope struct {
	Type      string        `json:"type"`
	SessionID string        `json:"sessionID,omitempty"`
	Part      *opencodePart `json:"part,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type opencodePart struct {
	ID       string          `json:"id,omitempty"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool,omitempty"`
	State    *opencodeState  `json:"state,omitempty"`
	Path     string          `json:"path,omitempty"`
	Tokens   json.RawMessage `json:"tokens,omitempty"`
	Cost     float64         `json:"cost,omitempty"`
	Provider string          `json:"providerID,omitempty"`
	Model    string          `json:"modelID,omitempty"`
}

type opencodeState struct {
	Status string          `json:"status,omitempty"` // pending | running | completed | error
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func parseOpenCodeStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg opencodeEnvelope
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "text":
		// Assistant text. Each event is one chunk; multiple events form the
		// full message.
		if msg.Part != nil && msg.Part.Text != "" {
			handler(AgentEvent{Type: "text", Content: msg.Part.Text, Timestamp: time.Now()})
		}

	case "reasoning":
		// Chain-of-thought routes to "thinking" so the UI can render it in
		// the collapsible reasoning pane.
		if msg.Part != nil && msg.Part.Text != "" {
			handler(AgentEvent{
				Type:      "thinking",
				Content:   msg.Part.Text,
				Metadata:  map[string]interface{}{"streaming": true},
				Timestamp: time.Now(),
			})
		}

	case "tool_use":
		// Tool invocation. state.status drives tool_call vs tool_result;
		// "completed"/"error" emit a result envelope, everything else
		// (pending/running) emits a tool_call so the UI shows lifecycle.
		// part.id is the correlation id (NOT envelope-level).
		if msg.Part == nil {
			return
		}
		state := msg.Part.State
		if state == nil {
			state = &opencodeState{}
		}
		switch state.Status {
		case "completed", "error":
			handler(AgentEvent{
				Type:    "tool_result",
				Content: state.Output,
				Metadata: map[string]interface{}{
					"tool_use_id": msg.Part.ID,
					"tool_name":   msg.Part.ToolName,
					"status":      state.Status,
					"error":       state.Error,
				},
				Timestamp: time.Now(),
			})
		default:
			var input any
			if len(state.Input) > 0 {
				_ = json.Unmarshal(state.Input, &input)
			}
			handler(AgentEvent{
				Type:    "tool_call",
				Content: msg.Part.ToolName,
				Metadata: map[string]interface{}{
					"tool_name": msg.Part.ToolName,
					"tool_id":   msg.Part.ID,
					"input":     input,
				},
				Timestamp: time.Now(),
			})
		}

	case "step_finish":
		// Per-turn usage envelope. Note the underscore name — older docs +
		// this codebase's previous parser assumed "step-finish" with hyphen.
		// Real upstream uses snake_case.
		if msg.Part == nil {
			return
		}
		var tokens map[string]interface{}
		if len(msg.Part.Tokens) > 0 {
			_ = json.Unmarshal(msg.Part.Tokens, &tokens)
		}
		handler(AgentEvent{
			Type: "result",
			Metadata: map[string]interface{}{
				"subtype":  "step_finish",
				"tokens":   tokens,
				"cost_usd": msg.Part.Cost,
				"provider": msg.Part.Provider,
				"model":    msg.Part.Model,
				"is_error": false,
			},
			Timestamp: time.Now(),
		})

	case "step_start":
		// Quiet — boundary marker only.
		return

	case "error":
		// Fatal error envelope (data.error is a string, not nested).
		handler(AgentEvent{
			Type:      "error",
			Content:   msg.Error,
			Timestamp: time.Now(),
		})

	default:
		// Forward-compat: unknown top-level type preserved as system meta so
		// the journal sees it without polluting chat. Examples we may see in
		// future: session.idle, message.completed.
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Type,
			Metadata: map[string]interface{}{
				"subtype":    msg.Type,
				"session_id": msg.SessionID,
			},
			Timestamp: time.Now(),
		})
	}
}
