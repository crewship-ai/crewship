package llm

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

func usageObject(raw []byte, path ...string) map[string]any {
	if !utf8.Valid(raw) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, e := usageJSON(decoder, 0)
	if e != nil {
		return nil
	}
	if _, e = decoder.Token(); e != io.EOF {
		return nil
	}
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	object, _ := value.(map[string]any)
	return object
}
func usageJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, io.ErrUnexpectedEOF
	}
	token, e := decoder.Token()
	if e != nil {
		return nil, e
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	if delimiter == '{' {
		object := map[string]any{}
		for decoder.More() {
			token, e := decoder.Token()
			if e != nil {
				return nil, e
			}
			key, ok := token.(string)
			if !ok {
				return nil, io.ErrUnexpectedEOF
			}
			if _, exists := object[key]; exists {
				return nil, io.ErrUnexpectedEOF
			}
			value, e := usageJSON(decoder, depth+1)
			if e != nil {
				return nil, e
			}
			object[key] = value
		}
		end, e := decoder.Token()
		if e != nil || end != json.Delim('}') {
			return nil, io.ErrUnexpectedEOF
		}
		return object, nil
	}
	if delimiter == '[' {
		var array []any
		for decoder.More() {
			value, e := usageJSON(decoder, depth+1)
			if e != nil {
				return nil, e
			}
			array = append(array, value)
		}
		end, e := decoder.Token()
		if e != nil || end != json.Delim(']') {
			return nil, io.ErrUnexpectedEOF
		}
		return array, nil
	}
	return nil, io.ErrUnexpectedEOF
}
func usageCount(object map[string]any, key string, required bool) (int64, bool) {
	value, exists := object[key]
	if !exists {
		return 0, !required
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	count, e := number.Int64()
	return count, e == nil && count >= 0 && count <= 2<<20
}
func openAIUsageKnown(raw []byte) bool {
	usage := usageObject(raw, "usage")
	if usage == nil {
		return false
	}
	prompt, p := usageCount(usage, "prompt_tokens", true)
	_, c := usageCount(usage, "completion_tokens", true)
	if !p || !c {
		return false
	}
	details, exists := usage["prompt_tokens_details"]
	if !exists {
		return true
	}
	object, ok := details.(map[string]any)
	if !ok {
		return false
	}
	cached, ok := usageCount(object, "cached_tokens", true)
	return ok && cached <= prompt
}
func anthropicUsageKnown(raw []byte, path ...string) bool {
	usage := usageObject(raw, path...)
	if usage == nil {
		return false
	}
	total := int64(0)
	for _, key := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
		count, ok := usageCount(usage, key, key == "input_tokens")
		if !ok || total > 2<<20-count {
			return false
		}
		total += count
	}
	_, ok := usageCount(usage, "output_tokens", true)
	return ok
}
