package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

type artifactProjectTool struct {
	name, description string
	fields, required  []string
}

const artifactProjectAuthoringContract = " Give meaningful sections, cards, headings and buttons stable unique element IDs prefixed with artifact- (for example artifact-hero-heading). Exact element comments require these IDs. Do not rewrite existing IDs for unchanged elements when publishing; author these IDs in your files because the server never injects them."

const artifactDashboardAuthoringContract = ` For a Dashboard, include dashboard.json with {"schema_version":1,"collections":["sales"]} in the code files. Use declarative HTML bindings: data-source="sales" selects a declared collection, data-row="*" repeats a row template (or name an existing document ID), and data-field="revenue" selects one literal own JSON key. Use native meter elements for numeric charts. Keep rendering free of authored JavaScript and SDK calls. Put stable artifact- IDs on authored static titles or labels outside bound and repeated subtrees for comments. Update values with artifact_data_batch using current code base_version and exact document ifVersion; data changes preserve the code version.`

var artifactProjectTools = []artifactProjectTool{
	{"artifact_data_batch", "Atomically upsert bounded JSON dashboard documents in a declared collection using exact document ifVersion values (0 creates) and current code base_version. Does not publish a code version. A 409 requires deliberate reconciliation, never blind retry. Host attribution cannot be supplied." + artifactDashboardAuthoringContract, []string{"slug", "collection", "base_version", "documents"}, []string{"slug", "collection", "base_version", "documents"}},
	{"artifact_data_read", "Read one declared dashboard collection under the exact selected code version and original Page mandate. Shared readers only receive collections in the current shared manifest.", []string{"slug", "collection", "version"}, []string{"slug", "collection", "version"}},
	{"artifact_data_collections", "List dataset collections declared by the selected dashboard code version, under current Page read authority.", []string{"slug", "version"}, []string{"slug", "version"}},
	{"artifact_create", "Create one private multi-screen HTML project after explicit human consent for this exact live run. No owner or audience selection. Save all linked files in one atomic manifest. A workspace must enable this feature. Never claim success without a returned version." + artifactProjectAuthoringContract + artifactDashboardAuthoringContract, []string{"slug", "name", "entrypoint", "files", "message"}, []string{"slug", "name", "entrypoint", "files"}},
	{"artifact_publish", "Save a complete new atomic manifest with the exact base_version previously read. HTTP 409 preserves the current project: keep your candidate, explain the conflict and reconcile intentionally. Never blindly retry with a newer base. No shared-version change." + artifactProjectAuthoringContract, []string{"slug", "base_version", "entrypoint", "files", "message"}, []string{"slug", "base_version", "entrypoint", "files"}},
	{"artifact_versions", "Read a project, its complete current file manifest, retained versions and quota under this run's explicit Page mandate. An optional version reads that immutable retained snapshot. Shared and latest versions are separate.", []string{"slug", "version"}, []string{"slug"}},
	{"artifact_restore", "Restore a retained version as a NEW version using exact latest base_version; never deletes history or changes shared version. 409 requires deliberate reconciliation.", []string{"slug", "base_version", "version"}, []string{"slug", "base_version", "version"}},
	{"artifact_comments", "Read anchored comments addressed to exact project versions. Before editing, verify path, selector and quote against that exact snapshot. If the anchor no longer identifies the original element, ask for a fresh selection.", []string{"slug", "version"}, []string{"slug"}},
	{"artifact_reply", "Reply in the existing comment thread with the exact version changed and a concise explanation, or ask for a fresh selection. Does not resolve, dispatch or grant access.", []string{"slug", "comment_id", "text", "version", "needs_clarification"}, []string{"slug", "comment_id", "text", "version"}},
}

func artifactProjectMCPCatalog() []memoryMCPToolDescriptor {
	result := make([]memoryMCPToolDescriptor, 0, len(artifactProjectTools))
	for _, tool := range artifactProjectTools {
		properties := map[string]any{}
		for _, field := range tool.fields {
			property := map[string]any{"type": "string"}
			switch field {
			case "needs_clarification":
				property = map[string]any{"type": "boolean"}
			case "version", "base_version":
				property = map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}
			case "files":
				property = map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path", "content", "media_type"}, "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"}}}}
			case "documents":
				property = map[string]any{"type": "array", "minItems": 1, "maxItems": artifacts.MaxDataBatchWrites, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "ifVersion", "value"}, "properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "ifVersion": map[string]any{"type": "integer", "minimum": 0, "maximum": artifacts.MaxDataVersion - 1}, "value": map[string]any{"type": "object"}}}}
			}
			properties[field] = property
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": properties, "required": tool.required, "additionalProperties": false})
		result = append(result, memoryMCPToolDescriptor{Name: tool.name, Description: tool.description, InputSchema: schema})
	}
	return result
}

var artifactProjectPathID = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// DecodeDocumentDraft rejects duplicate keys at EVERY depth and invalid UTF8.
// Identity never comes from these arguments: the sidecar forwards the original
// run bearer and the host independently checks the current immutable mandate.
func artifactProjectMCPPacket(name string, raw []byte) ([]byte, bool, error) {
	var definition *artifactProjectTool
	for i := range artifactProjectTools {
		if artifactProjectTools[i].name == name {
			definition = &artifactProjectTools[i]
			break
		}
	}
	if definition == nil {
		return nil, false, nil
	}
	var fields map[string]json.RawMessage
	decode := func() error { return DecodeDocumentDraft(raw, &fields) }
	if strings.HasPrefix(name, "artifact_data_") {
		decode = func() error { return artifacts.DecodeDataJSON(raw, &fields, definition.fields...) }
	}
	if len(raw) > 1<<20 || decode() != nil || fields == nil {
		return nil, true, fmt.Errorf("arguments must be a bounded closed object without duplicate fields")
	}
	allowed := map[string]bool{}
	for _, field := range definition.fields {
		allowed[field] = true
	}
	for key, value := range fields {
		if !allowed[key] || string(value) == "null" {
			return nil, true, fmt.Errorf("unknown or null argument %q", key)
		}
		switch key {
		case "needs_clarification":
			var flag bool
			if json.Unmarshal(value, &flag) != nil {
				return nil, true, fmt.Errorf("needs_clarification must be a boolean")
			}
		case "base_version", "version":
			var version int64
			if json.Unmarshal(value, &version) != nil || version < 0 || version > 9007199254740991 {
				return nil, true, fmt.Errorf("%s must be a nonnegative safe integer", key)
			}
		case "files":
			var objects []map[string]json.RawMessage
			if json.Unmarshal(value, &objects) != nil {
				return nil, true, fmt.Errorf("files must be an array")
			}
			for _, object := range objects {
				if len(object) != 3 || object["path"] == nil || object["content"] == nil || object["media_type"] == nil {
					return nil, true, fmt.Errorf("file requires canonical path, content and media_type")
				}
				for _, field := range []string{"path", "content", "media_type"} {
					var text string
					if bytes.Equal(bytes.TrimSpace(object[field]), []byte("null")) || json.Unmarshal(object[field], &text) != nil {
						return nil, true, fmt.Errorf("file %s must be a non-null string", field)
					}
				}
			}
			var files []struct {
				Path      string `json:"path"`
				Content   string `json:"content"`
				MediaType string `json:"media_type"`
			}
			decoder := json.NewDecoder(bytes.NewReader(value))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&files) != nil || len(files) < 1 || len(files) > 256 {
				return nil, true, fmt.Errorf("files must be 1–256 closed file objects")
			}
		case "documents":
			var documents []artifacts.DataWrite
			if json.Unmarshal(value, &documents) != nil || len(documents) < 1 || len(documents) > artifacts.MaxDataBatchWrites {
				return nil, true, fmt.Errorf("documents must be a bounded array of closed CAS objects")
			}
		default:
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, true, fmt.Errorf("%s must be a string", key)
			}
			if (key == "slug" || key == "comment_id" || key == "collection") && (!artifactProjectPathID.MatchString(text) || key == "collection" && strings.HasPrefix(text, "@")) {
				return nil, true, fmt.Errorf("invalid %s", key)
			}
		}
	}
	for _, required := range definition.required {
		if _, ok := fields[required]; !ok {
			return nil, true, fmt.Errorf("%s required", required)
		}
	}
	var slug string
	_ = json.Unmarshal(fields["slug"], &slug)
	delete(fields, "slug")
	if name == "artifact_data_batch" {
		raw, err := artifacts.MarshalDataJSON(map[string]json.RawMessage{"base_version": fields["base_version"], "documents": fields["documents"]})
		if err != nil {
			return nil, true, err
		}
		if _, err := artifacts.DecodeDataBatch(raw); err != nil {
			return nil, true, err
		}
	} else if strings.HasPrefix(name, "artifact_data_") {
		var version int64
		if json.Unmarshal(fields["version"], &version) != nil || version < 1 {
			return nil, true, fmt.Errorf("exact positive code version required")
		}
	}
	marshal := json.Marshal
	if strings.HasPrefix(name, "artifact_data_") {
		marshal = artifacts.MarshalDataJSON
	}
	packet, err := marshal(map[string]any{"operation": name, "slug": slug, "payload": fields})
	return packet, true, err
}

func (s *Server) respondArtifactProjectMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	packet, handled, err := artifactProjectMCPPacket(name, raw)
	if !handled {
		return false
	}
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": err.Error()}))
		return true
	}
	token := bearerToken(r)
	_, slug, _, present, valid := s.identityForRunTokenCtx(r.Context(), token)
	if !strings.HasPrefix(token, "agtv2.") || !present || !valid || s.ipc == nil {
		s.writeRoutinesMCPToolResult(w, req, 403, mustJSON(map[string]string{"error": "current live run required"}))
		return true
	}
	limit := int64(2 << 20)
	if name == "artifact_data_read" {
		limit = artifacts.MaxDataResponseBytes
	}
	response, err := s.sendBoundedHostRequest(r, http.MethodPost, "/api/v1/internal/pages/artifact-tool", packet, limit, slug)
	if err != nil || response == nil || !json.Valid(response.body) {
		s.writeRoutinesMCPToolResult(w, req, 503, mustJSON(map[string]string{"error": "outcome unavailable; retain candidate and inspect project before retrying"}))
		return true
	}
	if strings.HasPrefix(name, "artifact_data_") {
		s.writeArtifactDataMCPResult(w, req, response.status, response.body)
	} else {
		s.writeRoutinesMCPToolResult(w, req, response.status, response.body)
	}
	return true
}

// Avoid HTML escaping expanding legitimate dataset strings in the JSON-RPC
// text envelope. The result is JSON content, never HTML or executable markup.
func (s *Server) writeArtifactDataMCPResult(w http.ResponseWriter, req memoryMCPRequest, status int, body []byte) {
	out := memoryMCPToolCallResult{Content: []memoryMCPToolCallContent{{Type: "text", Text: string(body)}}, IsError: status >= 400}
	result, err := artifacts.MarshalDataJSON(out)
	if err != nil {
		http.Error(w, "cannot encode data result", 500)
		return
	}
	body, err = artifacts.MarshalDataJSON(memoryMCPResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(result)})
	if err != nil {
		http.Error(w, "cannot encode data result", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
