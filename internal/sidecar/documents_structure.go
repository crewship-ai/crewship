package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
)

const documentStructureIntentLimit = 256 << 10

var documentRequestIdentity = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type documentAgentRequestContext struct {
	Kind      string `json:"kind"`
	RequestID string `json:"request_id"`
	PageID    string `json:"page_id"`
	Schema    int    `json:"schema"`
	BaseRev   int64  `json:"base_rev"`
}

// CLI and MCP both enter here with only operations. All immutable identity and
// preconditions come from this run's live, server-owned request context. The
// current epoch is transport only; it never substitutes a new base revision.
func (s *Server) handleDocumentStructure(w http.ResponseWriter, r *http.Request, slug, operation string) {
	reply := func(status int, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
	reject := func(status int, message string) {
		raw, _ := json.Marshal(map[string]string{"error": message})
		reply(status, raw)
	}
	var intent struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if operation == "agent-apply" {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, documentStructureIntentLimit+4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&intent) != nil || decoder.Decode(new(any)) != io.EOF || len(intent.Operations) == 0 || len(intent.Operations) > 256 {
			reject(400, "only 1..256 structural operations are accepted; identity and preconditions are supplied by the tool")
			return
		}
		operations, err := json.Marshal(intent.Operations)
		if err != nil || len(operations) > documentStructureIntentLimit {
			reject(413, "structural operations exceed 256 KiB")
			return
		}
	} else if operation == "agent-checkpoint" {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2))
		if err != nil || (len(raw) != 0 && string(raw) != "{}") {
			reject(400, "checkpoint has no model-supplied envelope")
			return
		}
	}
	current, err := s.boundedHostRequest(r, http.MethodGet, "/api/v1/internal/documents/"+slug, nil, 4<<20)
	if err != nil {
		reject(503, "original document request context unavailable")
		return
	}
	if current.status != 200 {
		reply(current.status, current.body)
		return
	}
	var state struct {
		PageID  string                       `json:"page_id"`
		Schema  int                          `json:"schema"`
		Rev     int64                        `json:"rev"`
		Epoch   string                       `json:"epoch"`
		Content json.RawMessage              `json:"content"`
		Outline json.RawMessage              `json:"outline"`
		Request *documentAgentRequestContext `json:"agent_request"`
	}
	if json.Unmarshal(current.body, &state) != nil || state.Request == nil || state.Request.Kind != "structure" || state.Request.Schema != 3 || !documentRequestIdentity.MatchString(state.Request.RequestID) || state.Request.PageID == "" || state.Request.PageID != state.PageID || state.Request.BaseRev < 0 || state.Request.BaseRev >= 9007199254740991 || state.Epoch == "" || len(state.Epoch) > 512 || state.Schema < 1 || state.Schema > 3 {
		reject(403, "original bound structural request context required")
		return
	}
	if operation == "agent-read" {
		// Model-facing read contains text and the original context, never Yjs bytes.
		body, _ := json.Marshal(state)
		reply(200, body)
		return
	}
	payload := map[string]any{"id": state.Request.RequestID + "-patch", "schema": 3, "page_id": state.Request.PageID, "request_id": state.Request.RequestID, "kind": "agent", "epoch": state.Epoch, "ifRev": state.Request.BaseRev, "operations": intent.Operations}
	suffix := "patch"
	if operation == "agent-checkpoint" {
		suffix = "checkpoint"
		payload = map[string]any{"id": state.Request.RequestID + "-checkpoint", "schema": 3, "page_id": state.Request.PageID, "epoch": state.Epoch, "ifRev": state.Request.BaseRev + 1}
	}
	body, err := json.Marshal(payload)
	if err != nil || len(body) > documentStructureIntentLimit+4096 {
		reject(400, "bounded structural intent required")
		return
	}
	result, err := s.boundedHostRequest(r, http.MethodPost, "/api/v1/internal/documents/"+slug+"/"+suffix, body, 1<<20)
	if err != nil {
		reject(503, "document operation outcome is unknown; retry the same operations without changing the request")
		return
	}
	reply(result.status, result.body)
}

func documentStructureOperationsSchema() map[string]any {
	stringField := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,80}$"} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	kind := func(value string) map[string]any { return map[string]any{"type": "string", "enum": []string{value}} }
	text := object(map[string]any{"type": kind("text"), "text": map[string]any{"type": "string", "minLength": 1}}, "type", "text")
	content := map[string]any{"type": "array", "items": text}
	plain := object(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"paragraph", "codeBlock"}}, "content": content}, "type")
	heading := object(map[string]any{"type": kind("heading"), "attrs": object(map[string]any{"level": map[string]any{"type": "integer", "enum": []int{1, 2, 3}}}, "level"), "content": content}, "type", "attrs")
	insert := object(map[string]any{"op": kind("insert_after"), "after_id": stringField(), "node": map[string]any{"oneOf": []any{plain, heading}}}, "op", "after_id", "node")
	remove := object(map[string]any{"op": kind("delete"), "id": stringField()}, "op", "id")
	move := object(map[string]any{"op": kind("move_before"), "id": stringField(), "before_id": stringField()}, "op", "id", "before_id")
	return map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": map[string]any{"oneOf": []any{insert, remove, move}}}
}
