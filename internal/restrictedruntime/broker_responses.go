//go:build linux

package restrictedruntime

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"unicode/utf8"
)

// ResponsesPolicy is supplied by the host authority, never by the task body.
// This first adapter accepts only self-contained text. Remote resources, tools,
// stored conversations and opaque reasoning state need separate provenance gates.
// MaxOutputTokens bounds one request, not the attempt's total spend.
type ResponsesPolicy struct {
	Model           string
	MaxOutputTokens int64
}

var responsesModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)

func (g HTTPGrant) validResponses(credentials []BrokerCredential) bool {
	p := g.Responses
	if p == nil || !responsesModel.MatchString(p.Model) || p.MaxOutputTokens < 1 || p.MaxOutputTokens > 32768 || g.URL != "https://api.openai.com/v1/responses" || g.Method != "POST" || g.ResponseMode != "sse" || g.CredentialID == "" {
		return false
	}
	for _, c := range credentials {
		if c.ID == g.CredentialID && c.Provider == "openai" && c.Delivery == "broker-bearer-v1" && !c.Refresh {
			return true
		}
	}
	return false
}

func narrowResponses(parent, child *ResponsesPolicy) bool {
	if parent == nil || child == nil {
		return parent == nil && child == nil
	}
	return parent.Model == child.Model && child.MaxOutputTokens > 0 && child.MaxOutputTokens <= parent.MaxOutputTokens
}

// responsesBody returns canonical JSON after a closed-schema validation. No
// unchecked client fields are forwarded. Duplicate keys (including escaped
// equivalents), case variants, trailing values and excessive nesting fail closed.
func (p ResponsesPolicy) responsesBody(raw []byte) ([]byte, error) {
	if len(raw) > 1<<20 || !utf8.Valid(raw) {
		return nil, ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := uniqueJSON(d, 0)
	if err != nil {
		return nil, ErrDenied
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, ErrDenied
	}
	m, ok := v.(map[string]any)
	if !ok || !onlyKeys(m, "model", "input", "instructions", "store", "stream", "max_output_tokens") || m["model"] != p.Model || m["store"] != false || m["stream"] != true || !responsesInput(m["input"]) {
		return nil, ErrDenied
	}
	if instructions, exists := m["instructions"]; exists {
		if _, ok := instructions.(string); !ok {
			return nil, ErrDenied
		}
	}
	limit := p.MaxOutputTokens
	if value, exists := m["max_output_tokens"]; exists {
		n, ok := value.(json.Number)
		if !ok {
			return nil, ErrDenied
		}
		limit, err = n.Int64()
		if err != nil || limit < 1 || limit > p.MaxOutputTokens {
			return nil, ErrDenied
		}
	}
	m["max_output_tokens"] = limit
	return json.Marshal(m)
}

func responsesInput(v any) bool {
	if _, ok := v.(string); ok {
		return true
	}
	items, ok := v.([]any)
	if !ok || len(items) == 0 || len(items) > 256 {
		return false
	}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok || !onlyKeys(m, "type", "role", "content") {
			return false
		}
		if kind, exists := m["type"]; exists && kind != "message" {
			return false
		}
		switch m["role"] {
		case "user", "assistant", "developer", "system":
		default:
			return false
		}
		if _, ok := m["content"].(string); ok {
			continue
		}
		parts, ok := m["content"].([]any)
		if !ok || len(parts) == 0 || len(parts) > 256 {
			return false
		}
		for _, part := range parts {
			text, ok := part.(map[string]any)
			if !ok || !onlyKeys(text, "type", "text") || (text["type"] != "input_text" && text["type"] != "output_text") {
				return false
			}
			if _, ok := text["text"].(string); !ok {
				return false
			}
		}
	}
	return true
}

func onlyKeys(m map[string]any, keys ...string) bool {
	for key := range m {
		found := false
		for _, allowed := range keys {
			found = found || key == allowed
		}
		if !found {
			return false
		}
	}
	return true
}

func uniqueJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, ErrDenied
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		m := make(map[string]any)
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, ErrDenied
			}
			if _, exists := m[name]; exists {
				return nil, ErrDenied
			}
			m[name], err = uniqueJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrDenied
		}
		return m, nil
	case json.Delim('['):
		var values []any
		for d.More() {
			value, err := uniqueJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrDenied
		}
		return values, nil
	default:
		if _, isDelimiter := token.(json.Delim); isDelimiter {
			return nil, ErrDenied
		}
		return token, nil
	}
}
