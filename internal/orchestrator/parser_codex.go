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

// codexItem is the nested payload for item.* events. Field names verified
// against codex-rs/exec/src/exec_events.rs (May 2026). Several fields the
// pre-fix parser assumed do not exist in real upstream output:
//   - command_execution emits `aggregated_output` + `exit_code` (NOT `output`)
//   - mcp_tool_call    emits `server` + `tool` + `arguments` + `result`
//     (NOT `name` + `args`)
//   - error item       emits `message` for the error text
type codexItem struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`

	// agent_message / reasoning
	Text  string `json:"text,omitempty"`
	Delta string `json:"delta,omitempty"`

	// command_execution
	Command          string `json:"command,omitempty"`
	AggregatedOutput string `json:"aggregated_output,omitempty"`
	ExitCode         int    `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`

	// file_change
	Path string `json:"path,omitempty"`

	// mcp_tool_call — canonical field names from codex-rs upstream
	Server    string          `json:"server,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    string          `json:"result,omitempty"`

	// error item
	Message string `json:"message,omitempty"`

	// web_search / generic args fallback (some Codex builds use `args`)
	Args json.RawMessage `json:"args,omitempty"`

	// todo_list
	Items json.RawMessage `json:"items,omitempty"`

	// collab_tool_call (multi-agent peer handoff)
	SenderThreadID    string          `json:"sender_thread_id,omitempty"`
	ReceiverThreadIDs json.RawMessage `json:"receiver_thread_ids,omitempty"`
	Prompt            string          `json:"prompt,omitempty"`
	AgentsStates      json.RawMessage `json:"agents_states,omitempty"`
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
		// Output lives in `aggregated_output` (NOT `output`) per upstream
		// codex-rs schema; the previous parser silently produced empty
		// tool_result content for every shell call.
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
				Content: item.AggregatedOutput,
				Metadata: map[string]interface{}{
					"tool_use_id": item.ID,
					"status":      item.Status,
					"exit_code":   item.ExitCode,
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
		// MCP-routed tool call. Upstream emits separate `server` + `tool`
		// fields (NOT a combined `name`); we render them as "server.tool" for
		// the chat UI display name. Arguments live in `arguments` (NOT `args`)
		// and the response in `result` (NOT `output`). The previous parser's
		// field assumptions were inherited from the (different) tool.call
		// shape and silently dropped both directions.
		toolDisplay := item.Tool
		if item.Server != "" {
			toolDisplay = item.Server + "." + item.Tool
		}
		switch envelopeType {
		case "item.started":
			var input any
			if len(item.Arguments) > 0 {
				_ = json.Unmarshal(item.Arguments, &input)
			} else if len(item.Args) > 0 {
				// fallback for older Codex builds
				_ = json.Unmarshal(item.Args, &input)
			}
			handler(AgentEvent{
				Type:    "tool_call",
				Content: toolDisplay,
				Metadata: map[string]interface{}{
					"tool_name":  toolDisplay,
					"tool_id":    item.ID,
					"input":      input,
					"transport":  "mcp",
					"mcp_server": item.Server,
					"mcp_tool":   item.Tool,
				},
				Timestamp: time.Now(),
			})
		case "item.completed":
			body := item.Result
			if body == "" {
				// some builds still use "output" — accept both
				body = item.AggregatedOutput
			}
			handler(AgentEvent{
				Type:    "tool_result",
				Content: body,
				Metadata: map[string]interface{}{
					"tool_use_id": item.ID,
					"transport":   "mcp",
				},
				Timestamp: time.Now(),
			})
		}

	case "todo_list":
		// Codex's plan/todo emit. Surface as system meta so the UI can render
		// it in the timeline without hijacking chat. Items shape is
		// `[{text, completed}]` per upstream.
		var items any
		if len(item.Items) > 0 {
			_ = json.Unmarshal(item.Items, &items)
		}
		handler(AgentEvent{
			Type:    "system",
			Content: "todo_list",
			Metadata: map[string]interface{}{
				"subtype": "todo_list",
				"items":   items,
			},
			Timestamp: time.Now(),
		})

	case "collab_tool_call":
		// Multi-agent peer handoff (Codex SDK's parallel agent feature).
		// Crewship's peer-orchestration would benefit from rendering this
		// in Crow's Nest, but for now just preserve the metadata.
		var receivers, agentStates any
		if len(item.ReceiverThreadIDs) > 0 {
			_ = json.Unmarshal(item.ReceiverThreadIDs, &receivers)
		}
		if len(item.AgentsStates) > 0 {
			_ = json.Unmarshal(item.AgentsStates, &agentStates)
		}
		handler(AgentEvent{
			Type:    "system",
			Content: "collab_tool_call",
			Metadata: map[string]interface{}{
				"subtype":             "collab_tool_call",
				"tool":                item.Tool,
				"sender_thread_id":    item.SenderThreadID,
				"receiver_thread_ids": receivers,
				"prompt":              item.Prompt,
				"agents_states":       agentStates,
				"status":              item.Status,
			},
			Timestamp: time.Now(),
		})

	case "error":
		// Item-level error envelope (NOT the top-level "type":"error"). Codex
		// emits these for non-fatal warnings like backpressure or transient
		// upstream failures inside a turn. Classifying as a hard error would
		// mis-fail otherwise-healthy runs (issue #19689) — surface as warning
		// so the UI shows it but the orchestrator doesn't bail.
		handler(AgentEvent{
			Type:    "system",
			Content: item.Message,
			Metadata: map[string]interface{}{
				"subtype": "warning",
				"item_id": item.ID,
			},
			Timestamp: time.Now(),
		})

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
