package main

// Prepared components for incremental artifact HTTP handlers. This slice adds
// no routes; handlers register their request/response mapping when introduced.
func artifactProjectCoreComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min}
	}
	stamp := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
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
	schemas["ArtifactProjectFileInput"] = object(map[string]any{"path": path(), "content": str(), "media_type": text(255)}, "path", "content", "media_type")
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
	schemas["ArtifactProjectAvailability"] = object(map[string]any{"enabled": boolean()}, "enabled")
	manifestProps := map[string]any{"entrypoint": path(), "files": files("ArtifactProjectFileInput"), "message": text(4096)}
	schemas["ArtifactProjectCreateRequest"] = object(mergeProps(manifestProps, map[string]any{
		"slug": map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$", "description": "New Page slug; reserved route names are refused."},
		"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "base_version": map[string]any{"type": "integer", "enum": []int{0}},
	}), "slug", "name", "entrypoint", "files")
	schemas["ArtifactProjectPublishRequest"] = object(mergeProps(manifestProps, map[string]any{"base_version": integer(1)}), "base_version", "entrypoint", "files")
	schemas["ArtifactProjectVersionRequest"] = object(map[string]any{"base_version": integer(1), "version": integer(1)}, "base_version", "version")
	schemas["ArtifactProjectConsentRequest"] = object(map[string]any{"run_id": str(), "page_id": str()}, "run_id")
	schemas["ArtifactProjectConsentResponse"] = object(map[string]any{"run_id": str(), "page_id": str(), "expires_at": stamp(), "authorized": boolean()}, "run_id", "page_id", "expires_at", "authorized")
	receipt := map[string]any{"page_id": str(), "slug": str(), "version": integer(1), "url": str()}
	schemas["ArtifactProjectCreateResponse"] = object(receipt, "page_id", "slug", "version", "url")
	schemas["ArtifactProjectMutationResponse"] = object(mergeProps(receipt, map[string]any{"actor": str()}), "page_id", "slug", "version", "url", "actor")
	schemas["ArtifactProjectShareResponse"] = object(map[string]any{"shared_version": integer(1), "url": str()}, "shared_version", "url")
	schemas["ArtifactProjectError"] = object(map[string]any{"error": str(), "code": str()}, "error")
	return schemas
}
