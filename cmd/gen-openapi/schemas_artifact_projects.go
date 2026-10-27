package main

// These contracts describe the current Page-backed project handlers, including
// the immutable shared pin, explicit run consent and version-bound comments.
// They deliberately contain no generic payload or caller-supplied authority.
func artifactProjectSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min}
	}
	stamp := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "maxLength": max, "description": "The server additionally enforces this bound in UTF-8 bytes."}
	}
	path := func() map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Canonical relative project path, at most512 UTF8 bytes; absolute paths, traversal, backslashes, query strings and fragments are refused."}
	}
	files := func(item string) map[string]any {
		return map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": ref(item), "description": "Complete atomic file set; omitted files are not in the new version. Paths must be unique. The default server version limit is32MiB of content."}
	}
	schemas := map[string]any{}
	schemas["ArtifactProjectFileInput"] = object(map[string]any{"path": path(), "content": str(), "media_type": text(255)}, "path")
	schemas["ArtifactProjectFile"] = object(map[string]any{"path": path(), "content": str(), "media_type": str()}, "path", "content", "media_type")
	schemas["ArtifactProjectManifest"] = object(map[string]any{
		"version": integer(1), "entrypoint": path(), "files": files("ArtifactProjectFile"), "created_at": stamp(), "message": str(), "restored_from": integer(0),
	}, "version", "entrypoint", "files", "created_at", "message", "restored_from")
	schemas["ArtifactProjectVersion"] = object(map[string]any{
		"version": integer(1), "entrypoint": path(), "message": str(), "actor": str(), "created_at": stamp(), "restored_from": integer(0),
	}, "version", "entrypoint", "message", "actor", "created_at", "restored_from")
	schemas["ArtifactProjectQuota"] = object(map[string]any{
		"used_bytes": integer(0), "limit_bytes": integer(0), "warning": boolean(), "retention": map[string]any{"type": "string", "description": "Visible keep-all policy; the server never silently deletes retained versions. Quota refusal preserves existing data."},
	}, "used_bytes", "limit_bytes", "warning", "retention")
	schemas["ArtifactProjectOrigin"] = object(map[string]any{"chat_id": str(), "agent_id": str()}, "chat_id", "agent_id")
	schemas["ArtifactProjectReply"] = object(map[string]any{
		"text": str(), "version": integer(1), "needs_clarification": boolean(), "author": str(), "created_at": stamp(),
	}, "text", "version", "author", "created_at")
	schemas["ArtifactProjectComment"] = object(map[string]any{
		"id": str(), "version": integer(1), "path": path(), "selector": str(), "quote": str(), "text": str(), "author": str(), "task_id": str(), "created_at": stamp(),
		"status":           map[string]any{"type": "string", "enum": []string{"comment", "pending", "working", "answered", "needs_clarification"}},
		"answered_version": integer(1), "replies": array(ref("ArtifactProjectReply")),
	}, "id", "version", "path", "selector", "quote", "text", "author", "created_at", "status", "replies")
	schemas["ArtifactProjectSnapshot"] = object(map[string]any{
		"page_id": str(), "name": str(), "slug": str(), "origin": map[string]any{"oneOf": []any{ref("ArtifactProjectOrigin"), map[string]any{"type": "null"}}},
		"enabled": boolean(), "can_write": boolean(), "latest_version": integer(1), "shared_version": integer(0), "manifest": ref("ArtifactProjectManifest"),
		"versions": array(ref("ArtifactProjectVersion")), "quota": ref("ArtifactProjectQuota"), "comments": array(ref("ArtifactProjectComment")),
	}, "page_id", "name", "slug", "origin", "enabled", "can_write", "latest_version", "shared_version", "manifest", "versions", "quota", "comments")
	schemas["ArtifactProjectAvailability"] = object(map[string]any{"enabled": boolean()}, "enabled")
	manifestProps := map[string]any{"entrypoint": path(), "files": files("ArtifactProjectFileInput"), "message": text(4096)}
	schemas["ArtifactProjectCreateRequest"] = object(mergeProps(manifestProps, map[string]any{
		"slug": map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$", "description": "New Page slug; reserved route names are refused."},
		"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "base_version": map[string]any{"type": "integer", "enum": []int{0}},
	}), "slug", "name", "entrypoint", "files")
	schemas["ArtifactProjectPublishRequest"] = object(mergeProps(manifestProps, map[string]any{"base_version": integer(0)}), "base_version", "entrypoint", "files")
	schemas["ArtifactProjectVersionRequest"] = object(map[string]any{"base_version": integer(1), "version": integer(1)}, "base_version", "version")
	schemas["ArtifactProjectCommentRequest"] = object(map[string]any{
		"version": integer(1), "path": path(), "selector": text(2000), "quote": text(2000), "text": text(10000), "send_to_agent": boolean(),
	}, "version", "path", "selector", "quote", "text")
	schemas["ArtifactProjectReplyRequest"] = object(map[string]any{"text": text(10000), "version": integer(1), "needs_clarification": boolean()}, "text", "version")
	schemas["ArtifactProjectConsentRequest"] = object(map[string]any{"run_id": str(), "page_id": str()}, "run_id")
	schemas["ArtifactProjectConsentResponse"] = object(map[string]any{"run_id": str(), "page_id": str(), "expires_at": stamp(), "authorized": boolean()}, "run_id", "page_id", "expires_at", "authorized")
	receipt := map[string]any{"page_id": str(), "slug": str(), "version": integer(1), "url": str()}
	schemas["ArtifactProjectCreateResponse"] = object(receipt, "page_id", "slug", "version", "url")
	schemas["ArtifactProjectMutationResponse"] = object(mergeProps(receipt, map[string]any{"actor": str()}), "page_id", "slug", "version", "url", "actor")
	schemas["ArtifactProjectShareResponse"] = object(map[string]any{"shared_version": integer(1), "url": str()}, "shared_version", "url")
	schemas["ArtifactProjectError"] = object(map[string]any{"error": str(), "code": str()}, "error")
	routes := map[string]DomainSchema{}
	add := func(method, endpoint, response, request, status string) {
		definition := DomainSchema{Response: ref(response), SuccessStatuses: []string{status}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}}
		if request != "" {
			definition.Request, definition.RequestRequired = ref(request), true
		} else {
			definition.NoRequest = true
		}
		routes[method+" "+endpoint] = definition
	}
	add("POST", "/api/v1/pages/artifacts", "ArtifactProjectCreateResponse", "ArtifactProjectCreateRequest", "201")
	add("GET", "/api/v1/pages/artifacts/availability", "ArtifactProjectAvailability", "", "200")
	add("POST", "/api/v1/pages/artifacts/consent", "ArtifactProjectConsentResponse", "ArtifactProjectConsentRequest", "200")
	base := "/api/v1/pages/{slug}/artifact"
	for _, suffix := range []string{"", "/versions", "/comments"} {
		add("GET", base+suffix, "ArtifactProjectSnapshot", "", "200")
	}
	add("POST", base+"/versions", "ArtifactProjectMutationResponse", "ArtifactProjectPublishRequest", "200")
	add("POST", base+"/restore", "ArtifactProjectMutationResponse", "ArtifactProjectVersionRequest", "200")
	add("POST", base+"/share", "ArtifactProjectShareResponse", "ArtifactProjectVersionRequest", "200")
	add("POST", base+"/comments", "ArtifactProjectComment", "ArtifactProjectCommentRequest", "201")
	add("POST", base+"/comments/{commentId}/reply", "ArtifactProjectReply", "ArtifactProjectReplyRequest", "200")
	return routes, schemas
}
