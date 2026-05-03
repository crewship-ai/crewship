package orchestrator

import (
	"encoding/json"
	"time"
)

// parseCodexStreamJSON parses one stdout line from `codex exec --json`. Schema
// is the envelope-and-item model used by the Rust port (codex-rs/exec/src/),
// NOT the Agents-SDK style we initially assumed. Reference:
// developers.openai.com/codex/noninteractive + cli/reference.
//
// Top-level event types (discriminator: "type"):
//
//   - thread.started      — session bootstrap; thread_id (NOT session_id!)
//   - turn.started        — turn boundary begin (no payload)
//   - turn.completed      — turn end + usage:
//     { input_tokens, cached_input_tokens,
//     output_tokens, reasoning_output_tokens }
//     (reasoning_output_tokens added v0.124.0)
//   - turn.failed         — turn error envelope
//   - item.started        — generic item begin
//   - item.updated        — item delta (e.g. text streaming)
//   - item.completed      — item finalised
//   - error               — CLI / model error
//
// Items carry their own discriminator in `item.type`:
//
//   - agent_message       — assistant text  → text events
//   - reasoning           — chain-of-thought → thinking events
//   - command_execution   — shell exec      → tool_call(shell) + tool_result
//   - file_change         — write/edit      → tool_call(file_edit)
//   - mcp_tool_call       — MCP server call → tool_call
//   - web_search          — web search tool → tool_call
//   - plan_update         — plan/todo edit  → system meta event
type codexEnvelope struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     *codexItem      `json:"item,omitempty"`
	Usage    json.RawMessage `json:"usage,omitempty"`
	Error    string          `json:"error,omitempty"`
	Model    string          `json:"model,omitempty"`
}

// codexItem is the nested payload for item.* events. Only fields we surface
// are typed; the rest is preserved via Raw for forward-compat (Codex adds new
// item subtypes between releases).
type codexItem struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type,omitempty"`
	Text    string          `json:"text,omitempty"`
	Delta   string          `json:"delta,omitempty"`
	Command string          `json:"command,omitempty"`
	Path    string          `json:"path,omitempty"`
	Output  string          `json:"output,omitempty"`
	Status  string          `json:"status,omitempty"`
	Name    string          `json:"name,omitempty"` // mcp_tool_call: server.tool name
	Args    json.RawMessage `json:"args,omitempty"`
}

func parseCodexStreamJSON(line []byte, handler EventHandler) {
	if handler == nil {
		return
	}

	var msg codexEnvelope
	if err := json.Unmarshal(line, &msg); err != nil {
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
		return
	}

	switch msg.Type {
	case "thread.started":
		handler(AgentEvent{
			Type:    "system",
			Content: "init",
			Metadata: map[string]interface{}{
				"subtype":   "init",
				"thread_id": msg.ThreadID,
				"model":     msg.Model,
			},
			Timestamp: time.Now(),
		})

	case "turn.started":
		// Quiet — no payload. Could surface as a "system" tick but it would
		// flood the journal on long runs. Drop silently.
		return

	case "turn.completed":
		// Terminal envelope per turn (a Codex run can have many turns when
		// chaining tool calls). Carries the usage block with token counts —
		// Paymaster reads this. Note cached_input_tokens (cache hit savings)
		// and reasoning_output_tokens (o1/o3 thinking tokens, billed
		// separately).
		var usage map[string]interface{}
		if len(msg.Usage) > 0 {
			_ = json.Unmarshal(msg.Usage, &usage)
		}
		handler(AgentEvent{
			Type: "result",
			Metadata: map[string]interface{}{
				"subtype":  "turn.completed",
				"usage":    usage,
				"is_error": false,
			},
			Timestamp: time.Now(),
		})

	case "turn.failed":
		var usage map[string]interface{}
		if len(msg.Usage) > 0 {
			_ = json.Unmarshal(msg.Usage, &usage)
		}
		handler(AgentEvent{
			Type:    "result",
			Content: msg.Error,
			Metadata: map[string]interface{}{
				"subtype":  "turn.failed",
				"usage":    usage,
				"is_error": true,
			},
			Timestamp: time.Now(),
		})

	case "item.started", "item.updated", "item.completed":
		if msg.Item == nil {
			return
		}
		handleCodexItem(msg.Type, msg.Item, handler)

	case "error":
		handler(AgentEvent{
			Type:      "error",
			Content:   msg.Error,
			Timestamp: time.Now(),
		})

	default:
		// Forward-compat: surface raw line so future event types are visible
		// in the journal even before we parse them.
		handler(AgentEvent{Type: "text", Content: string(line) + "\n", Timestamp: time.Now()})
	}
}

// handleCodexItem fans the nested item out by item.type, mapping each into
// the AgentEvent kinds the chat UI / Crow's Nest already understand.
func handleCodexItem(envelopeType string, item *codexItem, handler EventHandler) {
	switch item.Type {
	case "agent_message":
		// Streaming: item.updated with delta; item.completed with full text.
		// We emit per-update so the UI can render token-by-token. The final
		// completed event carries the assembled text — emit only if no
		// updates preceded it (heuristic: delta empty + text present).
		body := item.Delta
		if body == "" && envelopeType == "item.completed" {
			body = item.Text
		}
		if body != "" {
			handler(AgentEvent{Type: "text", Content: body, Timestamp: time.Now()})
		}

	case "reasoning":
		// o1/o3 chain-of-thought. Surfaces as "thinking" so the UI can render
		// it in the collapsible reasoning pane.
		body := item.Delta
		if body == "" && envelopeType == "item.completed" {
			body = item.Text
		}
		if body != "" {
			handler(AgentEvent{
				Type:      "thinking",
				Content:   body,
				Metadata:  map[string]interface{}{"streaming": envelopeType == "item.updated"},
				Timestamp: time.Now(),
			})
		}

	case "command_execution":
		// Shell command. item.started → tool_call, item.completed → tool_result.
		switch envelopeType {
		case "item.started":
			handler(AgentEvent{
				Type:    "tool_call",
				Content: "shell",
				Metadata: map[string]interface{}{
					"tool_name": "shell",
					"tool_id":   item.ID,
					"input":     map[string]interface{}{"command": item.Command},
				},
				Timestamp: time.Now(),
			})
		case "item.completed":
			handler(AgentEvent{
				Type:    "tool_result",
				Content: item.Output,
				Metadata: map[string]interface{}{
					"tool_use_id": item.ID,
					"status":      item.Status,
				},
				Timestamp: time.Now(),
			})
		}

	case "file_change":
		if envelopeType == "item.completed" {
			handler(AgentEvent{
				Type:    "tool_call",
				Content: "file_edit",
				Metadata: map[string]interface{}{
					"tool_name": "file_edit",
					"tool_id":   item.ID,
					"input":     map[string]interface{}{"path": item.Path},
				},
				Timestamp: time.Now(),
			})
		}

	case "mcp_tool_call":
		// MCP-routed tool call; item.Name is "server.tool".
		switch envelopeType {
		case "item.started":
			var input any
			if len(item.Args) > 0 {
				_ = json.Unmarshal(item.Args, &input)
			}
			handler(AgentEvent{
				Type:    "tool_call",
				Content: item.Name,
				Metadata: map[string]interface{}{
					"tool_name": item.Name,
					"tool_id":   item.ID,
					"input":     input,
					"transport": "mcp",
				},
				Timestamp: time.Now(),
			})
		case "item.completed":
			handler(AgentEvent{
				Type:    "tool_result",
				Content: item.Output,
				Metadata: map[string]interface{}{
					"tool_use_id": item.ID,
					"transport":   "mcp",
				},
				Timestamp: time.Now(),
			})
		}

	case "web_search":
		if envelopeType == "item.started" {
			var input any
			if len(item.Args) > 0 {
				_ = json.Unmarshal(item.Args, &input)
			}
			handler(AgentEvent{
				Type:    "tool_call",
				Content: "web_search",
				Metadata: map[string]interface{}{
					"tool_name": "web_search",
					"tool_id":   item.ID,
					"input":     input,
				},
				Timestamp: time.Now(),
			})
		}

	case "plan_update":
		// Plan / todo edits — surface as a system event so they show up in the
		// timeline without hijacking the chat.
		handler(AgentEvent{
			Type:    "system",
			Content: "plan_update",
			Metadata: map[string]interface{}{
				"subtype": "plan_update",
				"text":    item.Text,
			},
			Timestamp: time.Now(),
		})

	default:
		// Unknown item.type — preserve in journal.
		handler(AgentEvent{
			Type:    "system",
			Content: item.Type,
			Metadata: map[string]interface{}{
				"subtype":       item.Type,
				"envelope_type": envelopeType,
			},
			Timestamp: time.Now(),
		})
	}
}
