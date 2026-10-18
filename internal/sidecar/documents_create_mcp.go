package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

var documentCreateMCPFields = []string{"slug", "name", "mutation_id", "blocks"}

func documentCreateMCPCatalog() []memoryMCPToolDescriptor {
	props := map[string]any{}
	for _, key := range documentCreateMCPFields {
		p := map[string]any{"type": "string"}
		if key == "blocks" {
			p["type"] = "array"
			p["maxItems"] = 128
			p["minItems"] = 1
			p["items"] = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"paragraph", "heading", "bulletList", "orderedList", "codeBlock"}}, "text": map[string]any{"type": "string"}, "level": map[string]any{"type": "integer", "minimum": 1, "maximum": 3}, "items": map[string]any{"type": "array", "minItems": 1, "maxItems": 128, "items": map[string]any{"type": "string"}}}}
		}
		props[key] = p
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": documentCreateMCPFields, "additionalProperties": false})
	return []memoryMCPToolDescriptor{{Name: "doc_create", Description: "Create one complete private human-owned Docs from explicit pre-send chat permission for this live run. Supply title/name, slug, mutation_id and readable paragraph/heading/list/codeBlock blocks; the server supplies IDs and hashes. This creation-only permission cannot edit existing Pages or continue editing the created document. Exact payload/mutation_id retry only. No HTML, images or raw Yjs.", InputSchema: schema}}
}

func decodeDocumentCreateMCPArgs(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("arguments must be an object")
	}
	allowed := map[string]bool{}
	for _, key := range documentCreateMCPFields {
		allowed[key] = true
	}
	args := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || !allowed[key] {
			return nil, fmt.Errorf("unknown argument")
		}
		if _, exists := args[key]; exists {
			return nil, fmt.Errorf("duplicate argument")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if bytes.Equal(value, []byte("null")) {
			return nil, fmt.Errorf("null argument")
		}
		var typed any
		if json.Unmarshal(value, &typed) != nil {
			return nil, fmt.Errorf("invalid argument")
		}
		switch key {
		case "blocks":
			if _, ok := typed.([]any); !ok {
				return nil, fmt.Errorf("%s must be an array", key)
			}
		default:
			if _, ok := typed.(string); !ok {
				return nil, fmt.Errorf("%s must be a string", key)
			}
		}
		args[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("trailing arguments")
	}
	for _, key := range documentCreateMCPFields {
		if _, ok := args[key]; !ok {
			return nil, fmt.Errorf("%s required", key)
		}
	}
	return args, nil
}

type documentCreateMCPRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *documentCreateMCPRecorder) Header() http.Header { return w.header }
func (w *documentCreateMCPRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *documentCreateMCPRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(body) > 1<<20 {
		w.overflow = true
	} else if !w.overflow {
		_, _ = w.body.Write(body)
	}
	return len(body), nil
}

func (s *Server) respondDocumentCreateMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	if name != "doc_create" {
		return false
	}
	args, err := decodeDocumentCreateMCPArgs(raw)
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": err.Error()}))
		return true
	}
	payload, _ := json.Marshal(args)
	forward := r.Clone(r.Context())
	forward.Method = http.MethodPost
	forward.URL, _ = url.Parse("/api/v1/documents/create")
	forward.Body = io.NopCloser(bytes.NewReader(payload))
	forward.ContentLength = int64(len(payload))
	forward.Header = r.Header.Clone()
	forward.Header.Set("Content-Type", "application/json")
	recorder := &documentCreateMCPRecorder{header: http.Header{}}
	s.handleDocumentCreate(recorder, forward)
	status := recorder.status
	if status == 0 {
		status = 200
	}
	if recorder.overflow {
		s.writeRoutinesMCPToolResult(w, req, 413, mustJSON(map[string]string{"error": "HTTP result body exceeds 1 MiB; use sidecar HTTP. If a mutation was sent, retain the same mutation_id to check its outcome."}))
		return true
	}
	result := map[string]any{"status": status}
	if json.Valid(recorder.body.Bytes()) {
		result["body"] = json.RawMessage(recorder.body.Bytes())
	} else {
		result["body"] = recorder.body.String()
	}
	if retry := recorder.header.Get("Retry-After"); retry != "" {
		result["retry_after"] = retry
	}
	s.writeRoutinesMCPToolResult(w, req, status, mustJSON(result))
	return true
}
