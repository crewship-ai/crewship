package orchestrator

import (
	"encoding/json"
	"time"
)

// parseGeminiStreamJSON parses one stdout line from `gemini -p X
// --output-format stream-json`. Schema verified against
// google-gemini/gemini-cli PR #10883 + geminicli.com/docs/cli/headless.
//
// Event types (discriminator: "type"):
//
//   - init        — { timestamp, session_id, model }
//   - message     — { role, content, timestamp, delta? }
//     (delta carries streaming text when --output-format
//     stream-json — easy to miss; without reading delta the
//     parser silently drops streaming output)
//   - tool_use    — { tool_name, tool_id, parameters, timestamp }
//     (NOT name/id/input — the snake_case names are canonical)
//   - tool_result — { tool_id, status, output, timestamp }
//     (tool_id, NOT tool_use_id; status carries success/error)
//   - error       — partially-documented error envelope
//   - result      — { status, stats:{ total_tokens, input_tokens,
//     output_tokens, duration_ms,
//     tool_calls }, timestamp }
type geminiStreamMessage struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	Severity  string `json:"severity,omitempty"` // PR #26262: warning/error on error envelopes
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`

	// message
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	Delta   string `json:"delta,omitempty"`
	Text    string `json:"text,omitempty"` // some builds use text instead of content

	// tool_use — note canonical snake_case field names from PR #10883
	ToolName   string          `json:"tool_name,omitempty"`
	ToolID     string          `json:"tool_id,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`

	// tool_result
	Output string `json:"output,omitempty"`
	Status string `json:"status,omitempty"`

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
		// Order of preference: delta (streaming), then content (full message),
		// then text (some pre-PR-10883 builds). Without checking delta first
		// we silently drop streamed token output — PR #10883 added it
		// specifically for incremental UX and gemini-cli started using it
		// over `content` in stream-json mode.
		body := msg.Delta
		if body == "" {
			body = msg.Content
		}
		if body == "" {
			body = msg.Text
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "tool_use":
		var input any
		if len(msg.Parameters) > 0 {
			_ = json.Unmarshal(msg.Parameters, &input)
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
				// Internal name kept as tool_use_id (matches Claude Code +
				// Cursor for chat-bridge correlation), value comes from
				// Gemini's tool_id field.
				"tool_use_id": msg.ToolID,
				"status":      msg.Status,
			},
			Timestamp: time.Now(),
		})

	case "error":
		// PR #26262 (gemini-cli 0.40.1, 2026-04-30) added a `severity` field
		// to error envelopes. severity="warning" means the model surfaced a
		// soft block / advisory (e.g. AgentExecutionBlocked) — NOT a hard
		// failure. Demote those to a system event so chat UI doesn't render
		// them in red and the orchestrator doesn't mark the run as failed.
		// (Pre-fix bug: previous parser checked msg.Subtype against "warning"
		// which is a different field — the demote was dead code.)
		eventType := "error"
		if msg.Severity == "warning" {
			eventType = "system"
		}
		handler(AgentEvent{
			Type:    eventType,
			Content: msg.Error,
			Metadata: map[string]interface{}{
				"subtype":  msg.Subtype,
				"severity": msg.Severity,
			},
			Timestamp: time.Now(),
		})

	case "result":
		var stats map[string]interface{}
		if len(msg.Stats) > 0 {
			_ = json.Unmarshal(msg.Stats, &stats)
		}
		handler(AgentEvent{
			Type:    "result",
			Content: msg.Response,
			Metadata: map[string]interface{}{
				"stats":    stats,
				"status":   msg.Status,
				"is_error": msg.Status == "error" || msg.Error != "",
			},
			Timestamp: time.Now(),
		})

	default:
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
