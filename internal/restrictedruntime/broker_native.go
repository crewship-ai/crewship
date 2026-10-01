//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// NativePolicy opts into the separate Codex scratch-tool protocol. Instructions,
// initial input, and conversation state remain private to the host authority.
type NativePolicy struct {
	Model             string
	MaxOutputTokens   int64
	InputTokenCeiling int64
}

// Opaque reasoning may represent more decoded input than its wire byte length.
// Native calls reserve the full known model context rather than guessing a ratio.
func NativeContextCeiling(model string) (int64, bool) {
	m, ok := modelcatalog.Default().Lookup("openai", model)
	return m.Limit.Context, ok && m.Limit.Context > 0 && m.Limit.Context <= 2<<20
}

func (g HTTPGrant) validNative(credentials []BrokerCredential) bool {
	if g.Native == nil {
		return false
	}
	ceiling, known := NativeContextCeiling(g.Native.Model)
	if !known || g.Native.InputTokenCeiling != ceiling {
		return false
	}
	copy := g
	copy.Responses = &ResponsesPolicy{Model: g.Native.Model, MaxOutputTokens: g.Native.MaxOutputTokens}
	copy.Native = nil
	return copy.validResponses(credentials)
}

func narrowNative(parent, child *NativePolicy) bool {
	if parent == nil || child == nil {
		return parent == nil && child == nil
	}
	return parent.Model == child.Model && parent.InputTokenCeiling == child.InputTokenCeiling && child.MaxOutputTokens > 0 && child.MaxOutputTokens <= parent.MaxOutputTokens
}

// NativeBrokerAuthority serializes each attempt's conversation before dispatch.
// An abandoned lease must never be recycled: upstream usage and issued state are
// unknown after a crash. Completing a lease records provenance before delivery.
type NativeBrokerAuthority interface {
	NativeRequest(context.Context, string, string, []byte) ([]byte, string, error)
	NativeComplete(context.Context, string, string, []json.RawMessage) error
}

// NativeConversation is trusted durable host state, never task metadata. Prefix
// is the CLI's first ambient-message prefix, which is never forwarded upstream.
// History contains only host-issued items and previously bound tool outputs.
type NativeConversation struct {
	Instructions string
	Input        string
	Prefix       []json.RawMessage
	History      []json.RawMessage
}

func nativeObject(raw []byte) (map[string]any, error) {
	if len(raw) > 1<<20 || !utf8.Valid(raw) {
		return nil, ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, e := uniqueJSON(d, 0)
	if e != nil {
		return nil, ErrDenied
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrDenied
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, ErrDenied
	}
	return m, nil
}

// NativeRequestBody reconstructs the provider request from frozen host context.
// It intentionally discards Codex's ambient instructions, tools, metadata, and
// cache identifiers. Follow-ups may append outputs only for exact issued calls.
func (p NativePolicy) NativeRequestBody(raw []byte, state NativeConversation) ([]byte, NativeConversation, error) {
	deny := func() ([]byte, NativeConversation, error) { return nil, state, ErrDenied }
	m, e := nativeObject(raw)
	if e != nil || !responsesModel.MatchString(p.Model) || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 32768 || !onlyKeys(m, "model", "instructions", "input", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "store", "stream", "include", "prompt_cache_key", "client_metadata") || m["model"] != p.Model || m["store"] != false || m["stream"] != true || m["tool_choice"] != "auto" || m["parallel_tool_calls"] != true {
		return deny()
	}
	if _, ok := m["instructions"].(string); !ok {
		return deny()
	}
	if v, ok := m["include"].([]any); !ok || len(v) != 1 || v[0] != "reasoning.encrypted_content" {
		return deny()
	}
	if r, ok := m["reasoning"].(map[string]any); !ok || !onlyKeys(r, "summary") || r["summary"] != "auto" {
		return deny()
	}
	if v, exists := m["prompt_cache_key"]; exists {
		if _, ok := v.(string); !ok {
			return deny()
		}
	}
	if v, exists := m["client_metadata"]; exists {
		if _, ok := v.(map[string]any); !ok {
			return deny()
		}
	}
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) > 16 {
		return deny()
	}
	for _, v := range tools {
		t, ok := v.(map[string]any)
		if !ok || !onlyKeys(t, "type", "name", "description", "parameters", "strict") || t["type"] != "function" {
			return deny()
		}
		switch t["name"] {
		case "exec_command", "write_stdin", "request_user_input", "view_image":
		default:
			return deny()
		}
	}
	items, ok := m["input"].([]any)
	if !ok || len(items) == 0 || len(items) > 256 {
		return deny()
	}
	var prefix, suffix []json.RawMessage
	seenState := false
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			return deny()
		}
		if item["type"] == "reasoning" {
			if content, exists := item["content"]; exists {
				if content != nil {
					return deny()
				}
				delete(item, "content")
			}
		}
		if item["type"] == "function_call_output" {
			if !onlyKeys(item, "type", "id", "call_id", "output") {
				return deny()
			}
			delete(item, "id")
		}
		encoded, e := json.Marshal(item)
		if e != nil {
			return deny()
		}
		if item["type"] == "message" && !seenState {
			if !nativeAmbientMessage(item) {
				return deny()
			}
			prefix = append(prefix, encoded)
		} else {
			seenState = true
			suffix = append(suffix, encoded)
		}
	}
	if len(state.Prefix) == 0 {
		if len(state.History) != 0 || len(suffix) != 0 || len(prefix) == 0 {
			return deny()
		}
		state.Prefix = prefix
	} else if !nativeEqualItems(prefix, state.Prefix) {
		return deny()
	}
	// Codex omits reasoning items with empty summaries. The host retains and
	// reconstructs every issued opaque item; any echoed item must match exactly.
	matched := 0
	for _, issued := range state.History {
		item, err := nativeObject(issued)
		if err != nil {
			return deny()
		}
		if matched < len(suffix) && nativeEqualItems(suffix[matched:matched+1], []json.RawMessage{issued}) {
			matched++
			continue
		}
		if item["type"] != "reasoning" {
			return deny()
		}
		if matched < len(suffix) {
			next, err := nativeObject(suffix[matched])
			if err != nil || next["type"] == "reasoning" {
				return deny()
			}
		}
	}
	pending := map[string]bool{}
	for _, rawItem := range state.History {
		item, e := nativeObject(rawItem)
		if e != nil {
			return deny()
		}
		if item["type"] == "function_call" {
			id, _ := item["call_id"].(string)
			pending[id] = true
		}
		if item["type"] == "function_call_output" {
			id, _ := item["call_id"].(string)
			delete(pending, id)
		}
	}
	outputs := suffix[matched:]
	if len(state.History) > 0 && (len(pending) == 0 || len(outputs) != len(pending)) {
		return deny()
	}
	for _, rawItem := range outputs {
		item, e := nativeObject(rawItem)
		if e != nil || !onlyKeys(item, "type", "id", "call_id", "output") || item["type"] != "function_call_output" {
			return deny()
		}
		id, ok := item["call_id"].(string)
		if !ok || !pending[id] {
			return deny()
		}
		if _, ok := item["output"].(string); !ok {
			return deny()
		}
		if value, exists := item["id"]; exists {
			if _, ok := value.(string); !ok {
				return deny()
			}
		}
		delete(pending, id)
		// CLI-generated output IDs carry no authority and are not forwarded.
		delete(item, "id")
		normalized, _ := json.Marshal(item)
		state.History = append(state.History, normalized)
	}
	input := []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": state.Input}}}}
	for _, item := range state.History {
		input = append(input, item)
	}
	body, e := json.Marshal(map[string]any{"model": p.Model, "instructions": state.Instructions, "input": input, "tools": nativeTools(), "tool_choice": "auto", "parallel_tool_calls": true, "reasoning": map[string]any{"summary": "auto"}, "include": []string{"reasoning.encrypted_content"}, "store": false, "stream": true, "max_output_tokens": p.MaxOutputTokens})
	if e != nil || len(body) > 1<<20 {
		return deny()
	}
	return body, state, nil
}

func nativeAmbientMessage(m map[string]any) bool {
	if !onlyKeys(m, "type", "role", "content", "id") {
		return false
	}
	if m["role"] != "developer" && m["role"] != "user" {
		return false
	}
	if id, exists := m["id"]; exists {
		if _, ok := id.(string); !ok && id != nil {
			return false
		}
	}
	parts, ok := m["content"].([]any)
	if !ok || len(parts) == 0 || len(parts) > 256 {
		return false
	}
	for _, v := range parts {
		p, ok := v.(map[string]any)
		if !ok || !onlyKeys(p, "type", "text") || p["type"] != "input_text" {
			return false
		}
		if _, ok := p["text"].(string); !ok {
			return false
		}
	}
	return true
}

func nativeEqualItems(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, e := nativeObject(a[i])
		if e != nil {
			return false
		}
		y, e := nativeObject(b[i])
		if e != nil {
			return false
		}
		xx, _ := json.Marshal(x)
		yy, _ := json.Marshal(y)
		if !bytes.Equal(xx, yy) {
			return false
		}
	}
	return true
}

func nativeTools() []any {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}
	exec := object(map[string]any{"cmd": str, "shell": str, "login": boolean, "workdir": str, "tty": boolean, "yield_time_ms": integer, "max_output_tokens": integer}, "cmd")
	write := object(map[string]any{"session_id": integer, "chars": str, "yield_time_ms": integer, "max_output_tokens": integer}, "session_id")
	return []any{map[string]any{"type": "function", "name": "exec_command", "description": "Execute a command inside this attempt's isolated scratch workspace using the configured sandbox.", "parameters": exec, "strict": false}, map[string]any{"type": "function", "name": "write_stdin", "description": "Interact with a process session owned by this attempt.", "parameters": write, "strict": false}}
}
