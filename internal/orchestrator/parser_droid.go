package orchestrator

import (
	"encoding/json"
	"time"
)

// parseDroidStreamJSON parses one stdout line from `droid exec -o stream-json`.
// Factory does not publish the event schema in the public CLI reference; this
// parser does best-effort extraction:
//
//   - Tries the common "type" discriminator across the field families used by
//     other coding-agent CLIs (text / tool_call / tool_result / result / error)
//     so that real captured Droid output starts surfacing into the chat UI
//     without a parser rewrite.
//   - Falls back to raw text for any unknown event shape so debug output is
//     still visible in the journal.
//
// Tightening this parser requires capturing a real `droid exec` run on dev2
// and turning it into a fixture (TODO post-smoke-test).
type droidStreamMessage struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Text    string          `json:"text,omitempty"`
	Content string          `json:"content,omitempty"`
	Delta   string          `json:"delta,omitempty"`
	Name    string          `json:"name,omitempty"`
	ID      string          `json:"id,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	Error   string          `json:"error,omitempty"`
	Usage   json.RawMessage `json:"usage,omitempty"`
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
	case "text", "message", "assistant":
		body := msg.Delta
		if body == "" {
			body = msg.Text
		}
		if body == "" {
			body = msg.Content
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "tool_call", "tool.call":
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

	case "tool_result", "tool.result":
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

	case "result", "complete", "completion":
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
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}
