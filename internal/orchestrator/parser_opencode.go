package orchestrator

import (
	"encoding/json"
	"time"
)

// parseOpenCodeStreamJSON consumes one stdout line from `opencode run --format
// json`. Unlike Cursor / Gemini / Claude Code, OpenCode does NOT stream — it
// buffers the whole response and emits a single JSON object at the end. We
// still receive that object via the same line-by-line streamOutput path
// (one big line), which lets us reuse the per-line interface uniformly.
//
// Because there's only one line per run, the parser fans the single payload
// out into multiple AgentEvents: one "text" for the response body, one
// "result" for usage + duration. If a future opencode release adds a true
// streaming JSONL mode we can detect and switch on a discriminator without
// changing the adapter contract.
type opencodeRunResult struct {
	// Documented fields from `opencode run --format json` output. The actual
	// shape is currently underspecified upstream; these are best-effort based
	// on the JSON-events behaviour the docs describe. We accept extra fields
	// silently (json.Unmarshal ignores unknowns) so a richer schema doesn't
	// break parsing.
	Response   string          `json:"response,omitempty"`
	Text       string          `json:"text,omitempty"`
	Output     string          `json:"output,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Model      string          `json:"model,omitempty"`
	Provider   string          `json:"provider,omitempty"`
	DurationMs float64         `json:"duration_ms,omitempty"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Error      string          `json:"error,omitempty"`
}

func parseOpenCodeStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg opencodeRunResult
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	body := msg.Response
	if body == "" {
		body = msg.Text
	}
	if body == "" {
		body = msg.Output
	}
	if body != "" {
		handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
	}

	if msg.Error != "" {
		handler(AgentEvent{Type: "error", Content: msg.Error, Timestamp: time.Now()})
	}

	var usage map[string]interface{}
	if len(msg.Usage) > 0 {
		_ = json.Unmarshal(msg.Usage, &usage)
	}
	handler(AgentEvent{
		Type:    "result",
		Content: body,
		Metadata: map[string]interface{}{
			"usage":       usage,
			"duration_ms": msg.DurationMs,
			"session_id":  msg.SessionID,
			"model":       msg.Model,
			"provider":    msg.Provider,
			"is_error":    msg.Error != "",
		},
		Timestamp: time.Now(),
	})
}
