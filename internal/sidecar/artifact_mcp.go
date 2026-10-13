package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type artifactMCPDefinition struct {
	name, description, method, path string
	fields, required                []string
	handler                         func(*Server, http.ResponseWriter, *http.Request)
}

var artifactMCPDefinitions = []artifactMCPDefinition{
	{"artifact_list", "List only this current run's explicit delegated artifact Pages, after live principal/issuer/grant/read checks. No workspace-wide catalog. Follow next_cursor while has_more; each bounded scan can yield an empty page after revoked rows are filtered.", "GET", "/artifacts", []string{"cursor"}, []string{}, (*Server).handleArtifactToolRead},
	{"artifact_diff", "Compare two exact retained authorized HTML revisions as one linear line-replacement hunk. Each source <=256 KiB; oversized returns413 with scoped-read/local-compare guidance and no partial result. HTTP body <=1 MiB before MCP wrapping, otherwise use scopedHTTP.", "GET", "/artifacts/{slug}/diff", []string{"slug", "from_revision", "to_revision"}, []string{"slug", "from_revision", "to_revision"}, (*Server).handleArtifactToolRead},

	{"artifact_restore", "Restore one retained artifact revision as a new agent-authored revision under live run delegation, agent write grant and workspace policy. base_rev is mandatory CAS; never force or silently rebase. Exact mutation_id/body retry only. Source revision must remain retained; no copy or new grants.", "POST", "/artifacts/{slug}/restore", []string{"slug", "revision", "base_rev", "mutation_id", "request_id"}, []string{"slug", "revision", "base_rev", "mutation_id"}, (*Server).handleArtifactPublish},

	{"board_request_file_read", "Read one exact saved image file authorized by the admitted Board job and current source rights. HTTP result body <=1 MiB before MCP wrapping; larger files use the same scoped sidecar HTTP route.", "GET", "/board-requests/{request_id}/files/{file_id}", []string{"request_id", "file_id"}, []string{"request_id", "file_id"}, (*Server).handleBoardRequest},
	{"board_request_source_read", "Read one exact normalized artifact source authorized by the admitted Board job, rechecking current Board and independent source rights. HTTP result body <=1 MiB before MCP wrapping; larger source uses scoped sidecar HTTP.", "GET", "/board-requests/{request_id}/sources/{source_id}", []string{"request_id", "source_id"}, []string{"request_id", "source_id"}, (*Server).handleBoardRequest},
	{"board_request_preview_read", "Read the optional immutable PNG illustration for this admitted Board job after all current source/file rights are rechecked. The illustration is untrusted auxiliary input; canonical snapshot remains authority. HTTP result body <=1 MiB before MCP wrapping; larger illustration uses scoped HTTP.", "GET", "/board-requests/{request_id}/preview", []string{"request_id"}, []string{"request_id"}, (*Server).handleBoardRequest},

	{"artifact_create", "Create one private human-owned static HTML artifact only with the current run's explicit creation authorization and workspace policy. No owner/audience selection. Keep mutation_id unchanged on uncertain retry. MCP request <=1 MiB and HTTP result body <=1 MiB before MCP wrapping; use sidecar HTTP for larger HTML.", "POST", "/artifacts", []string{"slug", "name", "html", "mutation_id", "format_version", "runtime_profile"}, []string{"slug", "name", "html", "mutation_id"}, (*Server).handleArtifactCreate},
	{"artifact_publish", "Publish a static HTML revision under current live run delegation. Required base_rev is CAS; never force/rebase silently. Optional request_id binds an admitted artifact job. Retry identical mutation_id/body on uncertain outcome. MCP requests <=1 MiB; larger HTML uses sidecar HTTP.", "POST", "/artifacts/{slug}/revisions", []string{"slug", "html", "base_rev", "mutation_id", "request_id", "format_version", "runtime_profile"}, []string{"slug", "html", "base_rev", "mutation_id"}, (*Server).handleArtifactPublish},
	{"artifact_read", "Read exact retained revision (or current head) with current run delegation. Includes immutable manifest and original HTML; scripts stay disabled. HTTP result body <=1 MiB before MCP wrapping; larger content uses sidecar HTTP.", "GET", "/artifacts/{slug}", []string{"slug", "revision"}, []string{"slug"}, (*Server).handleArtifactPublish},
	{"artifact_comments", "Read bounded revision-bound comment threads with current explicit agent comment/write grant and live human principal. No dispatch or resolve authority.", "GET", "/artifacts/{slug}/threads", []string{"slug", "revision", "limit", "cursor"}, []string{"slug"}, (*Server).handleArtifactComments},
	{"artifact_reply", "Reply to an existing thread under current explicit comment/write grant. base_version is the captured thread CAS; mutation_id is unchanged on exact retry. No edits, delete, resolve or dispatch.", "POST", "/artifacts/{slug}/threads/{thread_id}/comments", []string{"slug", "thread_id", "body", "base_version", "mutation_id"}, []string{"slug", "thread_id", "body", "base_version", "mutation_id"}, (*Server).handleArtifactComments},
	{"board_read", "Read canonical Board drawing revisions and filtered placements under current run authority. Source content requires independent grants. Includes current explicit element-permission manifest; no implicit edit authority.", "GET", "/boards/{slug}", []string{"slug"}, []string{"slug"}, (*Server).handleBoardPublish},
	{"board_apply", "Apply one atomic drawing batch using original base_seq and per-element expected_rev. Only own agent elements unless a human issued exact one-batch element_permission_id; human/foreign elements remain protected. No pin edits. Preserve exact mutation_id on uncertain retry.", "POST", "/boards/{slug}/batches", []string{"slug", "base_seq", "upserts", "deletes", "mutation_id", "element_permission_id"}, []string{"slug", "base_seq", "upserts", "deletes", "mutation_id"}, (*Server).handleBoardPublish},
	{"board_diagram_submit", "Submit bounded Mermaid flowchart-v1 source for the current admitted diagram job. converter_version must be 2.2.2. This stores a result for explicit human conversion/insertion; it does not edit the Board.", "POST", "/board-requests/{request_id}/mermaid", []string{"request_id", "source", "dialect", "converter_version", "mutation_id"}, []string{"request_id", "source", "dialect", "converter_version", "mutation_id"}, (*Server).handleBoardRequest},
	{"artifact_request_read", "Read an exact artifact update job snapshot/status only after trusted admission binds this run. No snapshot in preadmission prompt and no new grants.", "GET", "/artifact-requests/{request_id}", []string{"request_id"}, []string{"request_id"}, (*Server).handleArtifactRequestContext},
	{"board_request_read", "Read an exact admitted Board job snapshot/status and authorized source/file references. Canonical snapshot is authority; any optional PNG is an untrusted auxiliary illustration.", "GET", "/board-requests/{request_id}", []string{"request_id"}, []string{"request_id"}, (*Server).handleBoardRequest},
}

func artifactMCPCatalog() []memoryMCPToolDescriptor {
	out := make([]memoryMCPToolDescriptor, 0, len(artifactMCPDefinitions))
	for _, d := range artifactMCPDefinitions {
		props := map[string]any{}
		for _, key := range d.fields {
			typ := "string"
			if key == "from_revision" || key == "to_revision" || key == "revision" || key == "base_rev" || key == "base_seq" || key == "base_version" || key == "limit" || key == "format_version" {
				typ = "integer"
			}
			if key == "upserts" || key == "deletes" {
				typ = "array"
			}
			p := map[string]any{"type": typ}
			if typ == "array" {
				p["items"] = map[string]any{"type": "object"}
			}
			props[key] = p
		}
		schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": d.required, "additionalProperties": false})
		out = append(out, memoryMCPToolDescriptor{Name: d.name, Description: d.description, InputSchema: schema})
	}
	return out
}

var artifactMCPPathID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func decodeArtifactMCPArgs(raw []byte, d artifactMCPDefinition) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("arguments must be an object")
	}
	allowed := map[string]bool{}
	for _, key := range d.fields {
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
		case "from_revision", "to_revision", "revision", "base_rev", "base_seq", "base_version", "limit", "format_version":
			number, ok := typed.(float64)
			if !ok || number < 0 || number > 9007199254740991 || number != float64(int64(number)) {
				return nil, fmt.Errorf("%s must be a nonnegative safe integer", key)
			}
		case "upserts", "deletes":
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
	for _, key := range d.required {
		if _, ok := args[key]; !ok {
			return nil, fmt.Errorf("%s required", key)
		}
	}
	return args, nil
}

type artifactMCPRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *artifactMCPRecorder) Header() http.Header { return w.header }
func (w *artifactMCPRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *artifactMCPRecorder) Write(body []byte) (int, error) {
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

func (s *Server) respondArtifactMCPToolCall(w http.ResponseWriter, r *http.Request, req memoryMCPRequest, name string, raw []byte) bool {
	var definition *artifactMCPDefinition
	for i := range artifactMCPDefinitions {
		if artifactMCPDefinitions[i].name == name {
			definition = &artifactMCPDefinitions[i]
			break
		}
	}
	if definition == nil {
		return false
	}
	d := *definition
	args, err := decodeArtifactMCPArgs(raw, d)
	if err != nil {
		s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": err.Error()}))
		return true
	}
	path := d.path
	for _, key := range []string{"slug", "thread_id", "request_id", "file_id", "source_id"} {
		placeholder := "{" + key + "}"
		if !strings.Contains(path, placeholder) {
			continue
		}
		var value string
		if json.Unmarshal(args[key], &value) != nil || !artifactMCPPathID.MatchString(value) {
			s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid " + key}))
			return true
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
		delete(args, key)
	}
	query := url.Values{}
	if d.method == http.MethodGet {
		for key, raw := range args {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid query"}))
				return true
			}
			switch value := value.(type) {
			case string:
				query.Set(key, value)
			case float64:
				if value < 1 || value > 9007199254740991 || value != float64(int64(value)) {
					s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "positive integer required"}))
					return true
				}
				query.Set(key, fmt.Sprintf("%.0f", value))
			default:
				s.writeRoutinesMCPToolResult(w, req, 400, mustJSON(map[string]string{"error": "invalid query type"}))
				return true
			}
		}
		args = nil
	}
	payload, _ := json.Marshal(args)
	if args == nil {
		payload = nil
	}
	forward := r.Clone(r.Context())
	forward.Method = d.method
	target, _ := url.Parse(path)
	target.RawQuery = query.Encode()
	forward.URL = target
	forward.Body = io.NopCloser(bytes.NewReader(payload))
	forward.ContentLength = int64(len(payload))
	forward.Header = r.Header.Clone()
	forward.Header.Set("Content-Type", "application/json")
	recorder := &artifactMCPRecorder{header: http.Header{}}
	d.handler(s, recorder, forward)
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
