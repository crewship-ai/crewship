package sidecar

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

const mooringReadToolSchema = `{"type":"object","properties":{"page":{"type":"object","properties":{"cursor":{"type":"string","minLength":1,"maxLength":4096},"revision":{"type":"string","pattern":"^(0|[1-9][0-9]*)$"},"max_bytes":{"type":"integer","minimum":1024,"maximum":262144}},"additionalProperties":false}},"additionalProperties":false}`

// Only an immutable read cursor and response budget may cross this surface.
// Document, principal and host route always come from the original live task.
func mooringReadToolPath(raw json.RawMessage) (string, bool, error) {
	const path = "/api/v1/internal/mooring/task"
	invalid := errors.New("read accepts {} or a closed page object")
	var args map[string]json.RawMessage
	if decodeMooringMCP(raw, &args) != nil || args == nil {
		return "", false, invalid
	}
	if len(args) == 0 {
		return path, false, nil
	}
	if len(args) != 1 || args["page"] == nil {
		return "", false, invalid
	}
	var page map[string]json.RawMessage
	if decodeMooringMCP(args["page"], &page) != nil || page == nil {
		return "", false, invalid
	}
	query := url.Values{"page": {"1"}}
	for key, value := range page {
		switch key {
		case "cursor":
			var cursor string
			if json.Unmarshal(value, &cursor) != nil || cursor == "" || len(cursor) > 4096 {
				return "", false, invalid
			}
			query.Set(key, cursor)
		case "revision":
			var revision string
			if json.Unmarshal(value, &revision) != nil {
				return "", false, invalid
			}
			counter, err := strconv.ParseUint(revision, 10, 64)
			if err != nil || strconv.FormatUint(counter, 10) != revision {
				return "", false, invalid
			}
			query.Set(key, revision)
		case "max_bytes":
			var limit int
			if json.Unmarshal(value, &limit) != nil || limit < 1024 || limit > 256<<10 {
				return "", false, invalid
			}
			query.Set(key, strconv.Itoa(limit))
		default:
			return "", false, invalid
		}
	}
	if query.Has("cursor") && !query.Has("revision") {
		return "", false, invalid
	}
	return path + "?" + query.Encode(), true, nil
}

// max_bytes bounds the complete inner ReadResult. The existing MCP wrapper
// escapes it as a text string, so it has a separate hard outer-envelope bound.
// Measure the exact serialized envelope, including the caller's bounded ID.
func boundedMooringPageResult(req memoryMCPRequest, status int, body []byte) bool {
	result, err := json.Marshal(memoryMCPToolCallResult{
		Content: []memoryMCPToolCallContent{{Type: "text", Text: string(body)}},
		IsError: status >= 400,
	})
	if err != nil {
		return false
	}
	outer, err := json.Marshal(memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	return err == nil && len(outer) <= 2<<20
}
