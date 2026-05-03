package orchestrator

import (
	"encoding/json"
	"time"
)

// parseCursorStreamJSON parses one stdout line from `cursor-agent -p
// --output-format stream-json --stream-partial-output`. Schema documented at
// cursor.com/docs/cli/reference/output-format.
//
// Event types (discriminator: "type"):
//   - system    (subtype "init")        — apiKeySource, cwd, session_id, model
//   - user      message.role + content[]
//   - assistant message.role + content[] of {type:text,text:"..."} blocks
//     (with --stream-partial-output, may
//     include timestamp_ms + model_call_id)
//   - tool_call (subtype started/completed) — readToolCall / writeToolCall / function
//   - result    (subtype success/error) — duration_ms, duration_api_ms, is_error,
//     result, session_id
//
// We map these to the same AgentEvent kinds the Claude Code parser emits so
// the chat UI / Crow's Nest do not need a Cursor-specific reader.
type cursorStreamMessage struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype,omitempty"`
	Message *cursorMessage `json:"message,omitempty"`
	Result  string         `json:"result,omitempty"`
	IsError bool           `json:"is_error,omitempty"`
	// system init fields
	Model          string `json:"model,omitempty"`
	APIKeySource   string `json:"apiKeySource,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	// result fields
	DurationMs    float64 `json:"duration_ms,omitempty"`
	DurationAPIMs float64 `json:"duration_api_ms,omitempty"`
	// RequestID surfaces in the result envelope (success or error). Captured
	// for error correlation when reporting issues to Cursor support.
	RequestID string `json:"request_id,omitempty"`
	// ModelCallID + TimestampMs ride along assistant deltas when
	// --stream-partial-output is on. Useful for delta dedup on the chat-bridge
	// side if the connection reconnects mid-turn (Cursor's known
	// tool_call:completed loss bug, forum #157593, can also drop assistant
	// deltas on reconnect — chat-bridge can use these to skip duplicates).
	ModelCallID string  `json:"model_call_id,omitempty"`
	TimestampMs float64 `json:"timestamp_ms,omitempty"`
	// tool_call fields (subtype-dependent shape — keep raw for forward-compat)
	ToolCall json.RawMessage `json:"tool_call,omitempty"`
}

type cursorMessage struct {
	Role    string                 `json:"role"`
	Content []cursorMessageContent `json:"content"`
}

type cursorMessageContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func parseCursorStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg cursorStreamMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		// Not JSON — surface as plain text so the user still sees something
		// instead of silent loss. Same fallback policy as Claude Code parser.
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "system":
		// Session bootstrap. Useful for the system pane in Crow's Nest.
		handler(AgentEvent{
			Type:    "system",
			Content: msg.Subtype,
			Metadata: map[string]interface{}{
				"subtype":         msg.Subtype,
				"model":           msg.Model,
				"session_id":      msg.SessionID,
				"cwd":             msg.CWD,
				"permission_mode": msg.PermissionMode,
				"api_key_source":  msg.APIKeySource,
			},
			Timestamp: time.Now(),
		})

	case "assistant":
		// Assistant text. With --stream-partial-output enabled, deltas arrive
		// as multiple assistant messages with incremental text payloads — we
		// emit each as its own text event so the UI can render token-by-token
		// without buffering. model_call_id + timestamp_ms ride along for
		// chat-bridge dedup across reconnects (Cursor forum #157593).
		if msg.Message == nil {
			return
		}
		meta := map[string]interface{}{}
		if msg.ModelCallID != "" {
			meta["model_call_id"] = msg.ModelCallID
		}
		if msg.TimestampMs != 0 {
			meta["timestamp_ms"] = msg.TimestampMs
		}
		for _, block := range msg.Message.Content {
			if block.Type == "text" && block.Text != "" {
				ev := AgentEvent{Type: "text", Content: block.Text, Timestamp: time.Now()}
				if len(meta) > 0 {
					ev.Metadata = meta
				}
				handler(ev)
			}
		}

	case "user":
		// Cursor echoes the user message back. We don't surface this to chat
		// (would create a duplicate of what the user just typed) but if a
		// future feature wants to detect it the raw line is in the journal.
		return

	case "tool_call":
		// Tool invocation. Subtype "started" begins the call, "completed"
		// includes the result. We emit tool_call for started and tool_result
		// for completed so the UI can show the lifecycle. The raw tool_call
		// blob carries the per-tool details (file path for read/write, args
		// for function calls); we attach it as metadata for the chat-bridge
		// to render however it likes.
		var meta map[string]interface{}
		if len(msg.ToolCall) > 0 {
			_ = json.Unmarshal(msg.ToolCall, &meta)
		}
		if meta == nil {
			meta = map[string]interface{}{}
		}
		meta["subtype"] = msg.Subtype
		eventType := "tool_call"
		if msg.Subtype == "completed" {
			eventType = "tool_result"
		}
		handler(AgentEvent{
			Type:      eventType,
			Content:   msg.Subtype,
			Metadata:  meta,
			Timestamp: time.Now(),
		})

	case "result":
		// Terminal event with usage + duration. Mirrors Claude Code's "result"
		// shape so Paymaster can read both providers through one code path.
		// request_id captured for error correlation when filing Cursor support
		// tickets — without it the user has nothing to give the support team.
		handler(AgentEvent{
			Type:    "result",
			Content: msg.Result,
			Metadata: map[string]interface{}{
				"subtype":         msg.Subtype,
				"duration_ms":     msg.DurationMs,
				"duration_api_ms": msg.DurationAPIMs,
				"is_error":        msg.IsError,
				"session_id":      msg.SessionID,
				"request_id":      msg.RequestID,
			},
			Timestamp: time.Now(),
		})

	default:
		// Unknown type — log to journal via raw text fallback so we have
		// something to debug from. Keeps forward compat with Cursor adding
		// new event types between releases.
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
