package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The host feature gate and exact live-run mandate remain authoritative. A tool
// being discoverable never grants the run access to a canvas.
var designTools = []artifactProjectTool{
	{"design_get", "Read the current live Design nodes, order, edit permission and version under this run's exact Page mandate. Optional version reads an immutable retained snapshot. Preserve existing node IDs when applying changes.", []string{"slug", "version"}, []string{"slug"}},
	{"design_apply", "Apply operations directly to the same live Yjs canvas seen by people, including when no browser is connected. upsert contains a node (id, type frame|rect|text|image, name, x, y, width, height, fill, optional parentId, text, src); delete contains id; reorder contains order. Preserve existing node IDs and change only intended nodes. Save a snapshot after completing an edit.", []string{"slug", "operations", "message"}, []string{"slug", "operations"}},
	{"design_snapshot", "Save the current live Yjs canvas as an ordinary retained artifact version, without changing its shared version. Report success only after receiving the version receipt.", []string{"slug", "message"}, []string{"slug"}},
	{"design_versions", "List retained Design artifact versions under the exact run mandate.", []string{"slug"}, []string{"slug"}},
	{"design_restore", "Restore a retained Design snapshot as a new live version. Never deletes retained history. Requires the current exact write mandate.", []string{"slug", "version"}, []string{"slug", "version"}},
}

func designMCPCatalog() []memoryMCPToolDescriptor {
	result := make([]memoryMCPToolDescriptor, 0, len(designTools))
	for _, tool := range designTools {
		properties := map[string]any{}
		for _, field := range tool.fields {
			property := map[string]any{"type": "string"}
			switch field {
			case "version":
				property = map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}
			case "operations":
				property = map[string]any{"type": "array", "minItems": 1, "maxItems": 500, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"op"}, "properties": map[string]any{"op": map[string]any{"type": "string", "enum": []string{"upsert", "delete", "reorder"}}, "node": map[string]any{"type": "object", "description": "Node fields: id, type, name, x, y, width, height, fill, optional parentId, text, src"}, "id": map[string]any{"type": "string"}, "order": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}}
			}
			properties[field] = property
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": tool.required, "properties": properties})
		result = append(result, memoryMCPToolDescriptor{Name: tool.name, Description: tool.description, InputSchema: schema})
	}
	return result
}

func designMCPPacket(name string, raw []byte) ([]byte, bool, error) {
	var definition *artifactProjectTool
	for i := range designTools {
		if designTools[i].name == name {
			definition = &designTools[i]
			break
		}
	}
	if definition == nil {
		return nil, false, nil
	}
	if len(raw) > 1<<20 {
		return nil, true, fmt.Errorf("Design arguments exceed 1 MiB")
	}
	// Bound depth before the shared recursive duplicate-key decoder. A large
	// but byte-bounded argument must not exhaust the sidecar's call stack.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	depth := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, true, fmt.Errorf("invalid Design arguments")
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			if depth > 128 {
				return nil, true, fmt.Errorf("Design argument nesting exceeds 128 levels")
			}
		}
	}
	var fields map[string]json.RawMessage
	if DecodeDocumentDraft(raw, &fields) != nil || fields == nil {
		return nil, true, fmt.Errorf("arguments must be a bounded closed object without duplicate fields")
	}
	allowed := map[string]bool{}
	for _, field := range definition.fields {
		allowed[field] = true
	}
	for key, value := range fields {
		if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, true, fmt.Errorf("unknown or null argument %q", key)
		}
		switch key {
		case "version":
			var version int64
			if json.Unmarshal(value, &version) != nil || version < 1 || version > 9007199254740991 {
				return nil, true, fmt.Errorf("version must be a positive safe integer")
			}
		case "operations":
			var operations []map[string]json.RawMessage
			if json.Unmarshal(value, &operations) != nil || len(operations) < 1 || len(operations) > 500 {
				return nil, true, fmt.Errorf("operations must contain 1–500 changes")
			}
			for _, operation := range operations {
				var op string
				if json.Unmarshal(operation["op"], &op) != nil {
					return nil, true, fmt.Errorf("operation requires op")
				}
				for field := range operation {
					if field != "op" && field != "node" && field != "id" && field != "order" {
						return nil, true, fmt.Errorf("unknown operation field %q", field)
					}
				}
				switch op {
				case "upsert":
					var node map[string]any
					if len(operation) != 2 || json.Unmarshal(operation["node"], &node) != nil || node == nil {
						return nil, true, fmt.Errorf("upsert requires one node object")
					}
				case "delete":
					var id string
					if len(operation) != 2 || json.Unmarshal(operation["id"], &id) != nil || id == "" {
						return nil, true, fmt.Errorf("delete requires id")
					}
				case "reorder":
					var order []string
					if len(operation) != 2 || json.Unmarshal(operation["order"], &order) != nil || order == nil {
						return nil, true, fmt.Errorf("reorder requires order")
					}
				default:
					return nil, true, fmt.Errorf("unknown design operation")
				}
			}
		default:
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, true, fmt.Errorf("%s must be a string", key)
			}
			if key == "slug" && !artifactProjectPathID.MatchString(text) {
				return nil, true, fmt.Errorf("invalid slug")
			}
			if key == "name" && strings.TrimSpace(text) == "" {
				return nil, true, fmt.Errorf("name must not be empty")
			}
		}
	}
	for _, field := range definition.required {
		if fields[field] == nil {
			return nil, true, fmt.Errorf("%s required", field)
		}
	}
	var slug string
	_ = json.Unmarshal(fields["slug"], &slug)
	delete(fields, "slug")
	packet, err := json.Marshal(map[string]any{"operation": name, "slug": slug, "payload": fields})
	return packet, true, err
}

func (s *Server) respondDesignMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	packet, handled, err := designMCPPacket(name, raw)
	if !handled {
		return false
	}
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": err.Error()}))
		return true
	}
	token := bearerToken(r)
	_, workspace, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid || s.ipc == nil {
		s.writeRoutinesMCPToolResult(w, req, 403, mustJSON(map[string]string{"error": "current live run required"}))
		return true
	}
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/pages/design-tool", packet, 2<<20, workspace)
	if err != nil || response == nil || !json.Valid(response.body) {
		s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "outcome unavailable; inspect Design before retrying"}))
		return true
	}
	s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	return true
}
