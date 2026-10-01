package main

import "strconv"

// These contracts mirror the dedicated scoped store. No runtime handles,
// credential identity or unclassified shared-agent content appears on the wire.
func restrictedContextSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "format": "int64", "minimum": 0} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(fields map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": fields}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	request := func(fields map[string]any, required ...string) map[string]any {
		s := object(fields, required...)
		s["additionalProperties"] = false
		return s
	}
	content := func(limit int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": "Nonblank UTF-8 text, at most " + strconv.Itoa(limit) + " bytes."}
	}
	schemas := map[string]any{
		"RestrictedAgentProfile":        object(map[string]any{"profile": enum("disabled", "responses_text", "native_api_key")}, "profile"),
		"RestrictedAgentProfileRequest": object(map[string]any{"profile": enum("disabled", "responses_text", "native_api_key")}, "profile"),
		"RestrictedOperationMode":       object(map[string]any{"mode": enum("restricted", "trusted")}, "mode"),
		"RestrictedChatMode":            object(map[string]any{"mode": enum("restricted", "trusted"), "audience": enum("private", "group", "workspace")}, "mode", "audience"),
		"RestrictedCLIContextRequest":   request(map[string]any{}),
		"RestrictedCLIContext":          object(map[string]any{"id": str()}, "id"),
		"RestrictedRunRequest":          request(map[string]any{"content": content(32768), "project_file_versions": map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 96}, "maxItems": 16, "uniqueItems": true, "nullable": true, "description": "Explicit immutable project versions; native profile only. Empty by default."}}, "content"),
		"RestrictedMemoryRequest":       request(map[string]any{"content": content(8192)}, "content"),
		"RestrictedContextEntry":        object(map[string]any{"created_by": str(), "created_at": str(), "id": str(), "kind": enum("history", "memory", "summary"), "role": str(), "content": str(), "sources": array(str())}, "created_at", "id", "kind", "role", "content"),
		"RestrictedContextList":         object(map[string]any{"entries": array(ref("RestrictedContextEntry")), "limit": map[string]any{"type": "integer", "enum": []int{256}}}, "entries", "limit"),
		"RestrictedAttemptOutcome":      object(map[string]any{"state": enum("completed", "failed", "canceled", "denied"), "created_at": str(), "recorded_at": str()}, "state", "created_at", "recorded_at"),
		"RestrictedOutputFile":          object(map[string]any{"id": str(), "name": str(), "size_bytes": integer(), "sha256": str(), "created_at": str()}, "id", "name", "size_bytes", "sha256", "created_at"),
		"RestrictedOutputFiles":         object(map[string]any{"files": array(ref("RestrictedOutputFile"))}, "files"),
		"RestrictedIssueClaimRequest":   request(map[string]any{"agent_id": str(), "routine_slug": str()}, "agent_id", "routine_slug"),
		"RestrictedIssueClaimReceipt":   object(map[string]any{"run_id": str(), "chat_id": str(), "status": str(), "restricted": map[string]any{"type": "boolean", "enum": []bool{true}}}, "run_id", "chat_id", "status", "restricted"),
	}
	routes := map[string]DomainSchema{}
	add := func(method, path, req, res, status string) {
		s := DomainSchema{SuccessStatuses: []string{status}}
		if req != "" {
			s.Request = ref(req)
			s.RequestRequired = true
		}
		if res != "" {
			s.Response = ref(res)
		}
		routes[method+" "+path] = s
	}
	a := "/api/v1/agents/{agentId}"
	add("GET", a+"/restricted-execution", "", "RestrictedAgentProfile", "200")
	add("PUT", a+"/restricted-execution", "RestrictedAgentProfileRequest", "RestrictedAgentProfile", "200")
	add("GET", a+"/run-profile", "", "RestrictedOperationMode", "200")
	add("POST", a+"/restricted-cli-chats", "RestrictedCLIContextRequest", "RestrictedCLIContext", "201")
	c := "/api/v1/chats/{chatId}"
	add("GET", c+"/execution-profile", "", "RestrictedChatMode", "200")
	add("GET", c+"/restricted-context", "", "RestrictedContextList", "200")
	add("POST", c+"/restricted-memory", "RestrictedMemoryRequest", "RestrictedContextEntry", "201")
	add("DELETE", c+"/restricted-memory/{entryId}", "", "", "204")
	add("GET", c+"/restricted-files", "", "RestrictedOutputFiles", "200")
	routes["GET "+c+"/restricted-attempts"] = DomainSchema{Response: array(ref("RestrictedAttemptOutcome")), SuccessStatuses: []string{"200"}}
	routes["GET "+c+"/restricted-files/{fileId}/download"] = DomainSchema{Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/octet-stream"}, SuccessStatuses: []string{"200"}}
	for _, suffix := range []string{"/restricted-run", "/restricted-cli-run"} {
		routes["POST "+c+suffix] = DomainSchema{Request: ref("RestrictedRunRequest"), RequestRequired: true, Response: map[string]any{"type": "string", "description": "SSE data frames with type text/done; later errors close the stream."}, ResponseMedia: []string{"text/event-stream"}, SuccessStatuses: []string{"200"}}
	}
	add("POST", "/api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight", "RestrictedIssueClaimRequest", "RestrictedIssueClaimReceipt", "202")
	return routes, schemas
}
