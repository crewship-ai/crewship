//go:build linux

package restrictedruntime

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
)

// NativeTerminal validates complete host-observed provider output. Earlier SSE
// deltas are never forwarded: delivery is reconstructed from these vetted items.
// Opaque reasoning is admitted only here, then exact echo is checked by the next
// request against the attempt's durable issued history.
func NativeTerminal(raw []byte, model string) ([]json.RawMessage, []byte, error) {
	if len(raw) > 1<<20 {
		return nil, nil, ErrDenied
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	if !bytes.HasSuffix(raw, []byte("\n\n")) {
		return nil, nil, ErrDenied
	}
	var final map[string]any
	for _, frame := range bytes.Split(raw, []byte("\n\n")) {
		var data []byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data:")) {
				if data != nil {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
		}
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		event, e := nativeObject(data)
		if e != nil {
			return nil, nil, ErrDenied
		}
		switch event["type"] {
		case "response.failed", "response.incomplete", "error":
			return nil, nil, ErrDenied
		case "response.completed":
			if final != nil {
				return nil, nil, ErrDenied
			}
			var ok bool
			final, ok = event["response"].(map[string]any)
			if !ok {
				return nil, nil, ErrDenied
			}
		}
	}
	if final == nil || final["model"] != model || final["status"] != "completed" {
		return nil, nil, ErrDenied
	}
	id, ok := final["id"].(string)
	if !ok || !identifier.MatchString(id) {
		return nil, nil, ErrDenied
	}
	output, ok := final["output"].([]any)
	if !ok || len(output) == 0 || len(output) > 64 {
		return nil, nil, ErrDenied
	}
	var issued []json.RawMessage
	ids := map[string]bool{}
	calls := map[string]bool{}
	for _, v := range output {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, nil, ErrDenied
		}
		itemID, ok := item["id"].(string)
		if !ok || !identifier.MatchString(itemID) || ids[itemID] {
			return nil, nil, ErrDenied
		}
		ids[itemID] = true
		if status, exists := item["status"]; exists && status != "completed" {
			return nil, nil, ErrDenied
		}
		switch item["type"] {
		case "function_call":
			if !onlyKeys(item, "type", "id", "call_id", "name", "arguments", "status") {
				return nil, nil, ErrDenied
			}
			callID, ok := item["call_id"].(string)
			if !ok || !identifier.MatchString(callID) || calls[callID] {
				return nil, nil, ErrDenied
			}
			calls[callID] = true
			args, ok := item["arguments"].(string)
			if !ok || !nativeToolArguments(item["name"], []byte(args)) {
				return nil, nil, ErrDenied
			}
		case "reasoning":
			if !onlyKeys(item, "type", "id", "summary", "encrypted_content", "status") {
				return nil, nil, ErrDenied
			}
			encrypted, ok := item["encrypted_content"].(string)
			if !ok || len(encrypted) == 0 || len(encrypted) > 128<<10 {
				return nil, nil, ErrDenied
			}
			summary, ok := item["summary"].([]any)
			if !ok || len(summary) > 64 {
				return nil, nil, ErrDenied
			}
			for _, v := range summary {
				part, ok := v.(map[string]any)
				if !ok || !onlyKeys(part, "type", "text") || part["type"] != "summary_text" {
					return nil, nil, ErrDenied
				}
				if _, ok := part["text"].(string); !ok {
					return nil, nil, ErrDenied
				}
			}
		case "message":
			if !onlyKeys(item, "type", "id", "role", "content", "status") || item["role"] != "assistant" {
				return nil, nil, ErrDenied
			}
			content, ok := item["content"].([]any)
			if !ok || len(content) == 0 || len(content) > 64 {
				return nil, nil, ErrDenied
			}
			for _, v := range content {
				part, ok := v.(map[string]any)
				if !ok || !onlyKeys(part, "type", "text", "annotations") || part["type"] != "output_text" {
					return nil, nil, ErrDenied
				}
				if _, ok := part["text"].(string); !ok {
					return nil, nil, ErrDenied
				}
				if annotations, exists := part["annotations"]; exists {
					a, ok := annotations.([]any)
					if !ok || len(a) != 0 {
						return nil, nil, ErrDenied
					}
				}
			}
		default:
			return nil, nil, ErrDenied
		}
		// Codex echoes completed items without the server status field.
		delete(item, "status")
		encoded, e := json.Marshal(item)
		if e != nil {
			return nil, nil, ErrDenied
		}
		issued = append(issued, encoded)
	}
	// Emit a minimal fresh stream containing no unchecked vendor fields or deltas.
	var stream bytes.Buffer
	emit := func(kind string, fields map[string]any) {
		fields["type"] = kind
		payload, _ := json.Marshal(fields)
		stream.WriteString("event: " + kind + "\ndata: ")
		stream.Write(payload)
		stream.WriteString("\n\n")
	}
	emit("response.created", map[string]any{"response": map[string]any{"id": id, "model": model, "status": "in_progress", "output": []any{}}})
	for i, v := range output {
		item := v.(map[string]any)
		added := make(map[string]any, len(item))
		done := make(map[string]any, len(item))
		for k, v := range item {
			added[k] = v
			done[k] = v
		}
		if item["type"] == "function_call" {
			added["arguments"] = ""
			added["status"] = "in_progress"
			done["status"] = "completed"
		}
		if item["type"] == "message" {
			added["content"] = []any{}
			added["status"] = "in_progress"
			done["status"] = "completed"
		}
		emit("response.output_item.added", map[string]any{"output_index": i, "item": added})
		if item["type"] == "function_call" {
			emit("response.function_call_arguments.delta", map[string]any{"output_index": i, "item_id": item["id"], "delta": item["arguments"]})
			emit("response.function_call_arguments.done", map[string]any{"output_index": i, "item_id": item["id"], "arguments": item["arguments"]})
		}
		if item["type"] == "message" {
			for j, v := range item["content"].([]any) {
				part := v.(map[string]any)
				emit("response.content_part.added", map[string]any{"output_index": i, "item_id": item["id"], "content_index": j, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
				emit("response.output_text.delta", map[string]any{"output_index": i, "item_id": item["id"], "content_index": j, "delta": part["text"]})
				emit("response.output_text.done", map[string]any{"output_index": i, "item_id": item["id"], "content_index": j, "text": part["text"]})
				emit("response.content_part.done", map[string]any{"output_index": i, "item_id": item["id"], "content_index": j, "part": part})
			}
		}
		emit("response.output_item.done", map[string]any{"output_index": i, "item": done})
	}
	complete := map[string]any{"id": id, "model": model, "status": "completed", "output": output}
	// Usage is forwarded only after the separate trusted usage parser verifies it.
	usage := terminalResponsesUsage(raw, model)
	if usage.Known {
		complete["usage"] = map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.InputTokens + usage.OutputTokens, "input_tokens_details": map[string]any{"cached_tokens": usage.CachedInputTokens}}
	}
	emit("response.completed", map[string]any{"response": complete})
	if stream.Len() > 1<<20 {
		return nil, nil, ErrDenied
	}
	return issued, stream.Bytes(), nil
}

func nativeToolArguments(name any, raw []byte) bool {
	m, e := nativeObject(raw)
	if e != nil {
		return false
	}
	switch name {
	case "exec_command":
		if !onlyKeys(m, "cmd", "shell", "login", "workdir", "tty", "yield_time_ms", "max_output_tokens") {
			return false
		}
		cmd, ok := m["cmd"].(string)
		if !ok || len(cmd) == 0 || len(cmd) > 64<<10 || strings.ContainsRune(cmd, 0) {
			return false
		}
		if shell, exists := m["shell"]; exists && shell != "/bin/sh" && shell != "/bin/bash" {
			return false
		}
		if dir, exists := m["workdir"]; exists {
			s, ok := dir.(string)
			if !ok || path.Clean(s) != s || (s != "/home/agent" && !strings.HasPrefix(s, "/home/agent/")) {
				return false
			}
		}
		for _, k := range []string{"login", "tty"} {
			if v, exists := m[k]; exists {
				if _, ok := v.(bool); !ok {
					return false
				}
			}
		}
	case "write_stdin":
		if !onlyKeys(m, "session_id", "chars", "yield_time_ms", "max_output_tokens") {
			return false
		}
		n, ok := m["session_id"].(json.Number)
		if !ok {
			return false
		}
		id, e := n.Int64()
		if e != nil || id < 1 {
			return false
		}
		if chars, exists := m["chars"]; exists {
			s, ok := chars.(string)
			if !ok || len(s) > 64<<10 {
				return false
			}
		}
	default:
		return false
	}
	for key, bound := range map[string]int64{"yield_time_ms": 30000, "max_output_tokens": 10000} {
		if v, exists := m[key]; exists {
			n, ok := v.(json.Number)
			if !ok {
				return false
			}
			i, e := n.Int64()
			if e != nil || i < 1 || i > bound {
				return false
			}
		}
	}
	return true
}
