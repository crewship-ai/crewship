package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/scrubber"
)

const maxChatAuditBytes = 16 << 20

var errChatAuditUnavailable = errors.New("chat audit capture cannot be retained")

type chatAuditCapture struct {
	raw      bytes.Buffer
	overflow bool
}

func (a *chatAuditCapture) Write(p []byte) (int, error) {
	if !a.overflow {
		if len(p) > maxChatAuditBytes-a.raw.Len() {
			a.overflow = true
			a.raw = bytes.Buffer{}
		} else {
			_, _ = a.raw.Write(p)
		}
	}
	return len(p), nil
}

type auditFragmentKey struct {
	parent, index, field string
}

type auditFragment struct {
	fields map[string]any
	field  string
}

// redactChatAudit retains every wire envelope in order. Joined fragments are
// examined before any bytes leave memory because JSON delimiters can hide a
// secret split across deltas from an ordinary stream scrubber.
func redactChatAudit(raw []byte, secrets []string) ([]byte, error) {
	if len(raw) > maxChatAuditBytes {
		return nil, errChatAuditUnavailable
	}
	scrub := scrubber.New()
	scrub.AddSecretValues(secrets...)
	groups := make(map[auditFragmentKey][]auditFragment)
	seen := make(map[string]struct{})
	var frames []any
	flush := func(parent string) {
		for key, fragments := range groups {
			if key.parent == parent {
				redactAuditFragments(scrub, key.field, fragments)
				delete(groups, key)
			}
		}
	}
	for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if trimmed[0] != '{' && trimmed[0] != '[' {
			frames = append(frames, map[string]any{"type": "text_output", "payload_omitted": true})
			continue
		}
		value, err := decodeAuditJSON(line)
		frame, object := value.(map[string]any)
		if err != nil || !object {
			// This can include arbitrarily broken, partially decoded JSON.
			// Do not store its payload as an apparently safe diagnostic.
			return nil, errChatAuditUnavailable
		}
		frames = append(frames, frame)
		if uuid, _ := frame["uuid"].(string); uuid != "" {
			if _, replayed := seen[uuid]; replayed {
				// Keep replay envelopes for correlation. Their payload must
				// not break reconstruction of the original delta sequence.
				omitAuditFragmentPayload(frame, "type", "uuid", "parent_tool_use_id")
				continue
			}
			seen[uuid] = struct{}{}
		}
		parent, _ := frame["parent_tool_use_id"].(string)
		switch frame["type"] {
		case "assistant", "result":
			flush(parent)
		case "tool", "user":
			// Complete tool and user envelopes use structured redaction.
		case "system":
			switch frame["subtype"] {
			case "init", "api_retry":
				// Session and retry metadata are complete envelopes.
			default:
				omitAuditFragmentPayload(frame, "type", "subtype", "uuid", "parent_tool_use_id")
			}
		case "stream_event":
			event, _ := frame["event"].(map[string]any)
			switch event["type"] {
			case "message_start":
				flush(parent)
				if message, ok := event["message"].(map[string]any); ok {
					if content, isEmptyArray := message["content"].([]any); !isEmptyArray || len(content) > 0 {
						delete(message, "content")
						message["payload_omitted"] = true
					}
				}
			case "message_stop":
				flush(parent)
				omitAuditFragmentPayload(event, "type")
			case "content_block_start", "content_block_delta":
				recordAuditFragment(groups, parent, event)
			case "content_block_stop":
				omitAuditFragmentPayload(event, "type", "index")
			case "message_delta":
				// Preserve terminal metadata and aggregate usage.
				if delta, ok := event["delta"].(map[string]any); ok {
					omitAuditFragmentPayload(delta, "stop_reason", "stop_sequence")
				}
			default:
				omitAuditFragmentPayload(event, "type", "index")
			}
		default:
			omitAuditFragmentPayload(frame, "type", "uuid", "parent_tool_use_id")
		}
	}
	for key, fragments := range groups {
		redactAuditFragments(scrub, key.field, fragments)
	}
	for i, frame := range frames {
		frames[i] = scrubAuditJSON(scrub, frame)
	}
	if frames == nil {
		frames = []any{}
	}
	wire, err := json.Marshal(frames)
	if err != nil || len(wire) > maxChatAuditBytes {
		return nil, errChatAuditUnavailable
	}
	return wire, nil
}

func decodeAuditJSON(raw []byte) (any, error) {
	if !json.Valid(raw) {
		return nil, errChatAuditUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	err := d.Decode(&value)
	return value, err
}

func recordAuditFragment(groups map[auditFragmentKey][]auditFragment, parent string, event map[string]any) {
	index, indexed := event["index"].(json.Number)
	var fields map[string]any
	var field, group string
	if event["type"] == "content_block_start" {
		fields, _ = event["content_block"].(map[string]any)
		if fields == nil {
			omitAuditFragmentPayload(event, "type", "index")
			return
		}
		omitAuditFragmentPayload(event, "type", "index", "content_block")
		switch fields["type"] {
		case "text", "thinking":
			field, _ = fields["type"].(string)
			group = field
		case "tool_use":
			// An empty initial object is the normal preamble to JSON
			// deltas. A nonempty object is safe on its own, but cannot be
			// joined unambiguously to a subsequent delta sequence.
			if input, ok := fields["input"].(map[string]any); ok && len(input) == 0 && indexed {
				omitAuditFragmentPayload(fields, "type", "id", "name", "input")
				return
			}
			field, group = "input", "partial_json"
		}
	} else {
		fields, _ = event["delta"].(map[string]any)
		if fields == nil {
			omitAuditFragmentPayload(event, "type", "index")
			return
		}
		omitAuditFragmentPayload(event, "type", "index", "delta")
		switch fields["type"] {
		case "input_json_delta":
			field = "partial_json"
		case "text_delta":
			field = "text"
		case "thinking_delta":
			field = "thinking"
		}
		group = field
	}
	if field == "" || !indexed {
		omitAuditFragmentPayload(fields, "type", "id")
		return
	}
	omitAuditFragmentPayload(fields, "type", "id", "name", field)
	key := auditFragmentKey{parent, string(index), group}
	groups[key] = append(groups[key], auditFragment{fields, field})
}

func omitAuditFragmentPayload(fields map[string]any, structuralKeys ...string) {
	omitted := false
	for name := range fields {
		structural := false
		for _, key := range structuralKeys {
			if name == key {
				structural = true
				break
			}
		}
		if !structural {
			delete(fields, name)
			omitted = true
		}
	}
	if omitted {
		fields["payload_omitted"] = true
	}
}

func redactAuditFragments(scrub *scrubber.Scrubber, kind string, fragments []auditFragment) {
	if len(fragments) == 1 && fragments[0].field == "input" {
		return // The complete structured input is scrubbed with its envelope.
	}
	var joined strings.Builder
	for _, fragment := range fragments {
		text, ok := fragment.fields[fragment.field].(string)
		if !ok || fragment.field == "input" {
			for _, uncertain := range fragments {
				delete(uncertain.fields, uncertain.field)
				uncertain.fields["payload_omitted"] = true
			}
			return
		}
		joined.WriteString(text)
	}
	original := joined.String()
	redacted, _ := scrubAuditJSON(scrub, original).(string)
	if kind == "partial_json" {
		if _, err := decodeAuditJSON([]byte(original)); err != nil {
			redacted = "[REDACTED]"
		}
	}
	if redacted == original {
		return
	}
	for _, fragment := range fragments {
		fragment.fields[fragment.field] = ""
	}
	last := fragments[len(fragments)-1]
	last.fields[last.field] = redacted
}

func scrubAuditText(scrub *scrubber.Scrubber, value string) string {
	clean := scrub.Scrub(value)
	if strings.ContainsRune(value, '\\') {
		// A string field can hold a second JSON string encoding. Check its
		// decoded form too, retaining the original representation if safe.
		var decoded string
		if json.Unmarshal([]byte(`"`+value+`"`), &decoded) == nil && scrub.ContainsSecret(decoded) {
			return scrub.Scrub(decoded)
		}
	}
	return clean
}

func scrubAuditJSON(scrub *scrubber.Scrubber, value any) any {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			if nested, err := decodeAuditJSON([]byte(trimmed)); err == nil {
				clean := scrubAuditJSON(scrub, nested)
				before, _ := json.Marshal(nested)
				after, _ := json.Marshal(clean)
				if !bytes.Equal(before, after) {
					return string(after)
				}
			}
		}
		return scrubAuditText(scrub, v)
	case map[string]any:
		clean := make(map[string]any, len(v))
		for key, item := range v {
			cleanKey := scrubAuditText(scrub, key)
			switch strings.ToLower(cleanKey) {
			case "password", "secret", "token", "api_key", "apikey", "secret_key":
				clean[cleanKey] = "[REDACTED]"
			default:
				clean[cleanKey] = scrubAuditJSON(scrub, item)
			}
		}
		return clean
	case []any:
		clean := make([]any, len(v))
		for i, item := range v {
			clean[i] = scrubAuditJSON(scrub, item)
		}
		return clean
	default:
		return value
	}
}
