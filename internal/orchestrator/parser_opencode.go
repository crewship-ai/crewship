package orchestrator

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// parseOpenCodeStreamJSON parses one stdout line from `opencode run --format
// json`. Schema verified against the active upstream — github.com/sst/opencode
// (pre-2026) NOW REDIRECTS 301 → github.com/anomalyco/opencode and the
// pre-2026 opencode-ai/opencode npm package is archived. Either repo URL
// resolves; the npm `latest` tag remains opencode-ai@1.14.x. Current emitter
// is packages/opencode/src/cli/cmd/run.ts which writes JSON.stringify({
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
//   - error         — fatal error;                `error` is an object
//     ({name, data:{message,ref}}); legacy string form also accepted
//
// IDs: messageID and partID live INSIDE part (part.id), not at envelope
// level. The envelope only carries sessionID.
type opencodeEnvelope struct {
	Type      string        `json:"type"`
	SessionID string        `json:"sessionID,omitempty"`
	Part      *opencodePart `json:"part,omitempty"`
	// Error is deliberately json.RawMessage, not string: the real opencode
	// error envelope carries `error` as a NESTED OBJECT
	// ({name, data:{message,ref,...}}), and modelling it as a string made
	// json.Unmarshal of the WHOLE line fail — so every error fell through to
	// the plain-text branch, no "error" event fired, and streamOutput
	// synthesized a non-error terminal result that hid the cause (#1007).
	// decodeOpenCodeError handles both the object and legacy string forms.
	Error json.RawMessage `json:"error,omitempty"`
}

// opencodeErrorObject is the object form of the `error` envelope field
// observed from live opencode runs, e.g.
//
//	{"name":"APIError","data":{"message":"invalid x-api-key","statusCode":401}}
//	{"name":"UnknownError","data":{"message":"Unexpected server error...","ref":"err_..."}}
type opencodeErrorObject struct {
	Name string `json:"name"`
	Data struct {
		Message string `json:"message"`
		Ref     string `json:"ref"`
	} `json:"data"`
}

// decodeOpenCodeError renders a human-readable message from the `error`
// envelope field, accepting both the nested-object form (current opencode)
// and the legacy bare-string form. Returns "" when raw is empty. The object
// form is rendered as "Name: message (ref X)" so the operator sees the error
// class, the upstream message, and the correlation ref in one line.
func decodeOpenCodeError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Legacy/simple string form.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Current object form.
	var obj opencodeErrorObject
	if err := json.Unmarshal(raw, &obj); err == nil {
		msg := obj.Data.Message
		switch {
		case obj.Name != "" && msg != "":
			s = obj.Name + ": " + msg
		case msg != "":
			s = msg
		default:
			s = obj.Name
		}
		if obj.Data.Ref != "" {
			s = strings.TrimSpace(s + " (ref " + obj.Data.Ref + ")")
		}
		if s != "" {
			return s
		}
	}
	// Unknown shape — surface the raw JSON rather than dropping it.
	return string(raw)
}

type opencodePart struct {
	ID       string          `json:"id,omitempty"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"tool,omitempty"`
	State    *opencodeState  `json:"state,omitempty"`
	Path     string          `json:"path,omitempty"`
	Tokens   json.RawMessage `json:"tokens,omitempty"`
	Cost     float64         `json:"cost,omitempty"`
	Reason   string          `json:"reason,omitempty"` // step_finish: "stop" | "tool-calls"
	Provider string          `json:"providerID,omitempty"`
	Model    string          `json:"modelID,omitempty"`
}

// opencodeTextDedup tracks the last accumulated text seen per part so the
// parser can emit only the new suffix. OpenCode `text`/`reasoning` events
// carry the part's ACCUMULATED text so far (the TUI repaints the whole part),
// not deltas — emitting each event verbatim double-appends every chunk.
//
// State is package-level because adapters are stateless singletons shared
// across runs; keying by sessionID+partID keeps concurrent runs from
// colliding (part ids are unique per session). Entries are dropped at each
// step_finish for their session; the size cap is a backstop for streams that
// never reach a step boundary — on overflow the whole map resets, which at
// worst re-emits one full part once.
type opencodeTextDedup struct {
	mu   sync.Mutex
	seen map[string]string
}

const opencodeDedupCap = 4096

var opencodeDedup = &opencodeTextDedup{seen: make(map[string]string)}

// suffix returns the not-yet-emitted portion of text for the given part. If
// text does not extend what was seen before (delta-style upstream, or a
// rewrite), it returns text verbatim so nothing is dropped.
func (d *opencodeTextDedup) suffix(sessionID, partID, text string) string {
	if partID == "" {
		return text
	}
	key := sessionID + "\x00" + partID
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.seen) >= opencodeDedupCap {
		d.seen = make(map[string]string)
	}
	prev := d.seen[key]
	d.seen[key] = text
	if prev != "" && strings.HasPrefix(text, prev) {
		return text[len(prev):]
	}
	return text
}

// clearSession drops all dedup state for a session; called on step_finish so
// long-lived processes don't accumulate part keys.
func (d *opencodeTextDedup) clearSession(sessionID string) {
	prefix := sessionID + "\x00"
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.seen {
		if strings.HasPrefix(k, prefix) {
			delete(d.seen, k)
		}
	}
}

type opencodeState struct {
	Status string          `json:"status,omitempty"` // pending | running | completed | error
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// opencodeModelString renders the resolved model in OpenCode's own
// "provider/model" form (matches the --model flag), falling back to whichever
// half is present.
func opencodeModelString(p *opencodePart) string {
	switch {
	case p == nil:
		return ""
	case p.Provider != "" && p.Model != "":
		return p.Provider + "/" + p.Model
	case p.Model != "":
		return p.Model
	default:
		return ""
	}
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
		// Assistant text. Events carry the part's accumulated text so far —
		// emit only the new suffix (see opencodeTextDedup).
		if msg.Part != nil && msg.Part.Text != "" {
			if chunk := opencodeDedup.suffix(msg.SessionID, msg.Part.ID, msg.Part.Text); chunk != "" {
				handler(AgentEvent{Type: "text", Content: chunk, Timestamp: time.Now()})
			}
		}

	case "reasoning":
		// Chain-of-thought routes to "thinking" so the UI can render it in
		// the collapsible reasoning pane. Same accumulated-text semantics as
		// "text" parts.
		if msg.Part != nil && msg.Part.Text != "" {
			chunk := opencodeDedup.suffix(msg.SessionID, msg.Part.ID, msg.Part.Text)
			if chunk == "" {
				return
			}
			handler(AgentEvent{
				Type:      "thinking",
				Content:   chunk,
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
		// OpenCode has no init/bootstrap event; the resolved model rides on
		// step_finish metadata. Surface it as a system event (the shared
		// Accumulator captures the first one → run record + Crow's Nest).
		// Repeats on later steps are harmless: first capture wins.
		if model := opencodeModelString(msg.Part); model != "" {
			handler(AgentEvent{
				Type:    "system",
				Content: "init",
				Metadata: map[string]interface{}{
					"subtype":    "init",
					"model":      model,
					"session_id": msg.SessionID,
				},
				Timestamp: time.Now(),
			})
		}
		// Map usage into the keys the shared ParseResultUsage consumer reads
		// (total_cost_usd + usage.input_tokens/output_tokens — same contract
		// the Claude adapter emits). The raw OpenCode tokens map is kept for
		// fidelity (reasoning + cache breakdown).
		usage := map[string]interface{}{}
		if v, ok := tokens["input"]; ok {
			usage["input_tokens"] = v
		}
		if v, ok := tokens["output"]; ok {
			usage["output_tokens"] = v
		}
		handler(AgentEvent{
			Type: "result",
			Metadata: map[string]interface{}{
				"subtype":        "step_finish",
				"reason":         msg.Part.Reason,
				"total_cost_usd": msg.Part.Cost,
				"usage":          usage,
				"tokens":         tokens,
				"provider":       msg.Part.Provider,
				"model":          opencodeModelString(msg.Part),
				"is_error":       false,
			},
			Timestamp: time.Now(),
		})
		// Step boundary: previous parts won't stream further updates.
		opencodeDedup.clearSession(msg.SessionID)

	case "step_start":
		// Quiet — boundary marker only.
		return

	case "error":
		// Fatal error envelope. `error` is a nested object in current
		// opencode ({name, data:{message,ref}}); decodeOpenCodeError also
		// accepts the legacy string form (#1007).
		handler(AgentEvent{
			Type:      "error",
			Content:   decodeOpenCodeError(msg.Error),
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
