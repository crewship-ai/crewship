package orchestrator

import (
	"encoding/json"
	"time"
)

// parseOpenCodeStreamJSON parses one stdout line from `opencode run --format
// json`. Surprise from upstream docs validation (May 2026): this is NOT a
// single buffered JSON object — it's a JSONL stream of message-part events,
// one per line, modelled on OpenCode's internal Part type.
//
// Top-level event envelope:
//
//	{
//	  "type":      "message.part.updated",
//	  "timestamp": "2026-05-03T...",
//	  "sessionID": "ses-abc",   // CAMELCASE — not session_id
//	  "messageID": "msg-1",
//	  "partID":    "prt-1",
//	  "part":      { "type": "<part-type>", ... }
//	}
//
// Part types (discriminator inside `part.type`):
//
//   - text          — assistant text chunk
//   - reasoning     — chain-of-thought
//   - tool          — tool invocation (with nested .state.status started/completed)
//   - file          — file read/write summary
//   - subtask       — subagent call
//   - step-start    — model turn boundary begin
//   - step-finish   — model turn boundary end + token counts
//   - snapshot      — workspace snapshot ref (for fork/restore)
//   - patch         — file patch payload
//   - agent         — agent metadata
//   - retry         — retry attempt notice
//   - compaction    — context compaction event
type opencodeEnvelope struct {
	Type      string        `json:"type"`
	SessionID string        `json:"sessionID,omitempty"`
	MessageID string        `json:"messageID,omitempty"`
	PartID    string        `json:"partID,omitempty"`
	Part      *opencodePart `json:"part,omitempty"`
}

type opencodePart struct {
	Type     string          `json:"type,omitempty"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool,omitempty"`
	State    *opencodeState  `json:"state,omitempty"`
	Path     string          `json:"path,omitempty"`
	Tokens   json.RawMessage `json:"tokens,omitempty"`
	Cost     float64         `json:"cost,omitempty"`
	Provider string          `json:"providerID,omitempty"`
	Model    string          `json:"modelID,omitempty"`
	Error    string          `json:"error,omitempty"`
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

	// Some events (e.g. session.idle, message.completed) carry no Part;
	// surface them as system events for the journal but don't fan out.
	if msg.Part == nil {
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Type,
			Metadata: map[string]interface{}{
				"subtype":    msg.Type,
				"session_id": msg.SessionID,
				"message_id": msg.MessageID,
			},
			Timestamp: time.Now(),
		})
		return
	}

	switch msg.Part.Type {
	case "text":
		if msg.Part.Text != "" {
			handler(AgentEvent{Type: "text", Content: msg.Part.Text, Timestamp: time.Now()})
		}

	case "reasoning":
		if msg.Part.Text != "" {
			handler(AgentEvent{
				Type:      "thinking",
				Content:   msg.Part.Text,
				Metadata:  map[string]interface{}{"streaming": true},
				Timestamp: time.Now(),
			})
		}

	case "tool":
		// state.status drives tool_call vs tool_result. "completed" / "error"
		// emit the result envelope; everything else (pending/running) emits
		// a tool_call so the UI shows the lifecycle.
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
					"tool_use_id": msg.PartID,
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
					"tool_id":   msg.PartID,
					"input":     input,
				},
				Timestamp: time.Now(),
			})
		}

	case "step-finish":
		// Per-turn usage envelope. Emit as result so Paymaster can read it.
		// On a multi-turn run there will be multiple step-finish events; the
		// chat-bridge can sum or take the last.
		var tokens map[string]interface{}
		if len(msg.Part.Tokens) > 0 {
			_ = json.Unmarshal(msg.Part.Tokens, &tokens)
		}
		handler(AgentEvent{
			Type: "result",
			Metadata: map[string]interface{}{
				"subtype":  "step-finish",
				"tokens":   tokens,
				"cost_usd": msg.Part.Cost,
				"provider": msg.Part.Provider,
				"model":    msg.Part.Model,
				"is_error": false,
			},
			Timestamp: time.Now(),
		})

	case "step-start":
		// Quiet — boundary marker only.
		return

	case "file":
		// Summary that a file was read/written. Surface as a tool_result-ish
		// event so the Files panel + Crow's Nest can log it.
		handler(AgentEvent{
			Type:    "tool_result",
			Content: msg.Part.Path,
			Metadata: map[string]interface{}{
				"tool_use_id": msg.PartID,
				"tool_name":   "file",
				"path":        msg.Part.Path,
			},
			Timestamp: time.Now(),
		})

	case "subtask", "agent", "snapshot", "patch", "retry", "compaction":
		// Not chat-relevant; record as system meta for the journal.
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Part.Type,
			Metadata: map[string]interface{}{
				"subtype":    msg.Part.Type,
				"session_id": msg.SessionID,
				"part_id":    msg.PartID,
			},
			Timestamp: time.Now(),
		})

	default:
		// Unknown part.type — preserve in journal.
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Part.Type,
			Metadata: map[string]interface{}{
				"subtype":    msg.Part.Type,
				"session_id": msg.SessionID,
			},
			Timestamp: time.Now(),
		})
	}
}
