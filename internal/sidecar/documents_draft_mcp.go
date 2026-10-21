package sidecar

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
)

func documentDraftMCPCatalog() []memoryMCPToolDescriptor {
	stringField := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 0}
	object := func(props map[string]any) map[string]any {
		required := []string{}
		for key := range props {
			required = append(required, key)
		}
		sort.Strings(required)
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	patch := object(map[string]any{"id": stringField, "schema": map[string]any{"const": 3}, "page_id": stringField, "epoch": stringField, "ifRev": integer, "target": stringField, "ifHash": stringField, "text": stringField})
	pin := object(map[string]any{"id": stringField, "schema": map[string]any{"const": 3}, "page_id": stringField, "epoch": stringField, "ifRev": integer})
	packet := object(map[string]any{"generation": integer, "patch": patch, "checkpoint": pin})
	view := object(map[string]any{"page_id": stringField, "schema": map[string]any{"const": 3}, "rev": integer, "generation": integer, "epoch": stringField, "targets": map[string]any{"type": "array", "items": object(map[string]any{"id": stringField, "type": map[string]any{"enum": []string{"paragraph", "heading"}}, "parent": map[string]any{"type": []string{"string", "null"}}, "hash": stringField, "text": stringField})}})
	specs := []struct {
		name, description string
		props             map[string]any
	}{
		{"doc_draft_view", "Read only this live run's explicitly admitted newly created Document drafting view. No arbitrary Page, human profile, raw Yjs or renewed authority.", map[string]any{}},
		{"doc_draft_prepare", "Prepare UNSAVED replaceText and checkpoint packets from the permitted view. Select a paragraph/heading ID and text; the tool copies server hashes and original revision/generation/epoch. Retain this exact packet for uncertain retries.", map[string]any{"view": view, "target": stringField, "text": stringField, "mutation_id": stringField}},
		{"doc_draft_apply", "Apply an immutable prepared packet, then pin its committed revision. No reread or rederived retries. committed_checkpoint_pending means the patch is already durable; retry checkpoint with the same packet.", map[string]any{"packet": packet}},
		{"doc_draft_checkpoint", "Retry only the exact pin from a retained immutable packet. Does not apply new text or renew run authority; unknown responses do not mean rollback.", map[string]any{"packet": packet}},
	}
	tools := []memoryMCPToolDescriptor{}
	for _, s := range specs {
		schema, _ := json.Marshal(object(s.props))
		tools = append(tools, memoryMCPToolDescriptor{Name: s.name, Description: s.description, InputSchema: schema})
	}
	return tools
}

func (s *Server) respondDocumentDraftMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	if name != "doc_draft_view" && name != "doc_draft_prepare" && name != "doc_draft_apply" && name != "doc_draft_checkpoint" {
		return false
	}
	identity := &documentCreateMCPRecorder{header: http.Header{}}
	if !s.documentDraftIdentity(identity, r) {
		s.writeRoutinesMCPToolResult(w, req, 403, mustJSON(map[string]string{"error": "live run capability required"}))
		return true
	}
	fail := func(err error) {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": err.Error()}))
	}
	exchange := func(method string, body []byte) (int, []byte, error) {
		forward := r.Clone(r.Context())
		forward.Method = method
		forward.URL, _ = url.Parse("/api/v1/documents/draft")
		forward.Body = io.NopCloser(bytes.NewReader(body))
		forward.ContentLength = int64(len(body))
		forward.Header = r.Header.Clone()
		forward.Header.Set("Content-Type", "application/json")
		recorder := &documentCreateMCPRecorder{header: http.Header{}}
		s.handleDocumentDraft(recorder, forward)
		if recorder.overflow || recorder.body.Len() > 64<<10 {
			return 0, nil, errors.New("bounded draft response required")
		}
		status := recorder.status
		if status == 0 {
			status = 200
		}
		return status, recorder.body.Bytes(), nil
	}
	switch name {
	case "doc_draft_prepare":
		packet, err := PrepareDocumentDraft(raw)
		if err != nil {
			fail(err)
			return true
		}
		s.writeRoutinesMCPToolResult(w, req, 200, mustJSON(map[string]any{"status": "prepared_unsaved", "packet": packet}))
	case "doc_draft_view":
		args := map[string]json.RawMessage{}
		if err := DecodeDocumentDraft(raw, &args); err != nil || len(args) != 0 {
			fail(errors.New("draft view accepts an empty object only"))
			return true
		}
		status, body, err := exchange(http.MethodGet, nil)
		if err != nil || !json.Valid(body) {
			s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "draft view unavailable"}))
			return true
		}
		s.writeRoutinesMCPToolResult(w, req, status, body)
	default:
		var args struct {
			Packet json.RawMessage `json:"packet"`
		}
		if err := DecodeDocumentDraft(raw, &args); err != nil {
			fail(err)
			return true
		}
		p, err := ParseDocumentDraftPacket(args.Packet)
		if err != nil {
			fail(err)
			return true
		}
		out, err := ApplyDocumentDraft(p, name == "doc_draft_checkpoint", exchange)
		status := 200
		if err != nil {
			status = 503
		}
		s.writeRoutinesMCPToolResult(w, req, status, mustJSON(out))
	}
	return true
}
