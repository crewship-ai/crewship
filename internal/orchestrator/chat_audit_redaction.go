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
	var frames []any
	flush := func(parent string) {
		for key, fragments := range groups {
			if key.parent == parent {
				redactAuditFragments(scrub, fragments)
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
			frames = append(frames, map[string]any{"type": "text_output", "text": string(line)})
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
		parent, _ := frame["parent_tool_use_id"].(string)
		switch frame["type"] {
		case "assistant", "result":
			flush(parent)
		case "stream_event":
			event, _ := frame["event"].(map[string]any)
			switch event["type"] {
			case "message_start", "message_stop":
				flush(parent)
			case "content_block_delta":
				delta, _ := event["delta"].(map[string]any)
				index, _ := event["index"].(json.Number)
				field := ""
				switch delta["type"] {
				case "input_json_delta":
					field = "partial_json"
				case "text_delta":
					field = "text"
				case "thinking_delta":
					field = "thinking"
				}
				if field != "" {
					key := auditFragmentKey{parent, string(index), field}
					groups[key] = append(groups[key], auditFragment{delta, field})
				} else {
					for name := range delta {
						if name != "type" {
							delta[name] = "[REDACTED]"
						}
					}
				}
			}
		}
	}
	for _, fragments := range groups {
		redactAuditFragments(scrub, fragments)
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

func redactAuditFragments(scrub *scrubber.Scrubber, fragments []auditFragment) {
	var joined strings.Builder
	for _, fragment := range fragments {
		text, _ := fragment.fields[fragment.field].(string)
		joined.WriteString(text)
	}
	original := joined.String()
	redacted := scrubAuditText(scrub, original)
	if fragments[0].field == "partial_json" {
		value, err := decodeAuditJSON([]byte(original))
		if err != nil {
			redacted = "[REDACTED]"
		} else {
			clean := scrubAuditJSON(scrub, value)
			before, _ := json.Marshal(value)
			after, _ := json.Marshal(clean)
			if !bytes.Equal(before, after) {
				redacted = string(after)
			}
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
