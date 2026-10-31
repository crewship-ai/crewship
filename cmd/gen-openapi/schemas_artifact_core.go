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
		return map[string]any{"type": "string", "minLength": 1, "maxLength": 512, "description": "Canonical relative project path, at most 512 UTF-8 bytes; absolute paths, traversal, backslashes, query strings and fragments are refused."}
	}
	files := func(item string) map[string]any {
		return map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": ref(item), "description": "Complete atomic file set; omitted files are not in the new version. Paths must be unique. The default server version limit is 32 MiB of content."}
	}
	described := func(schema map[string]any, description string) map[string]any {
		schema["description"] = description
		return schema
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
		"used_bytes": integer(0), "limit_bytes": integer(0), "warning": map[string]any{"type": "boolean", "description": "True from the near threshold upwards; prefer level."},
		"level":         map[string]any{"type": "string", "enum": []string{"none", "near", "critical", "full"}, "description": "none below 80% of limit_bytes, near from 80%, critical from 95%, full when used_bytes reaches limit_bytes. Omitted when the quota is withheld (shared or editor views report limit_bytes 0)."},
		"storage_level": map[string]any{"type": "string", "enum": []string{"none", "near", "critical", "full"}, "description": "Coarse aggregate (workspace and instance) artifact storage level, using the same thresholds as level. It carries no byte counts; workspace owners and admins read those from GET /api/v1/pages/artifacts/storage. Omitted when the quota is withheld."},
		"retention":     map[string]any{"type": "string", "description": "Owner views use keep-all with history usage; non-owner editors use latest-version-only and other readers shared-version-only, with zero usage/limit values. These visibility labels do not prune stored versions; quota refusal preserves existing data."},
	}, "used_bytes", "limit_bytes", "warning", "retention")
	schemas["ArtifactProjectAvailability"] = object(map[string]any{"enabled": boolean(), "can_create": boolean(), "code": map[string]any{"type": "string", "enum": []string{"artifact_crew_not_allowed"}}}, "enabled")
	storageLevel := func() map[string]any {
		return map[string]any{"type": "string", "enum": []string{"none", "near", "critical", "full"}}
	}
	schemas["ArtifactStorageUsage"] = object(map[string]any{
		"used_bytes":     described(integer(0), "Bytes retained by every artifact family in this workspace: versions, proposals, Design, Data, documents, Slides, GridCraft and Mooring rows."),
		"reserved_bytes": described(integer(0), "Bytes held by in-flight uploads; released when they commit or end."),
		"limit_bytes":    integer(1),
		"level":          described(storageLevel(), "Workspace level from used_bytes+reserved_bytes against limit_bytes."),
		"instance_level": described(storageLevel(), "Coarse instance level; instance byte counts are never returned."),
		"retention":      map[string]any{"type": "string", "enum": []string{"keep-all"}, "description": "History is never deleted automatically."},
	}, "used_bytes", "reserved_bytes", "limit_bytes", "level", "instance_level", "retention")
	manifestProps := map[string]any{"entrypoint": path(), "files": files("ArtifactProjectFileInput"), "message": text(4096)}
	schemas["ArtifactProjectCreateRequest"] = object(mergeProps(manifestProps, map[string]any{
		"created_by_test": boolean(),
		"slug":            map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$", "description": "New Page slug; reserved route names are refused."},
		"name":            map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "base_version": map[string]any{"type": "integer", "enum": []int{0}, "description": "Creation has no existing base; if supplied this must be 0."},
	}), "slug", "name", "entrypoint", "files")
	schemas["ArtifactProjectPublishRequest"] = object(mergeProps(manifestProps, map[string]any{"base_version": described(integer(1), "Latest version the caller read; a stale value returns 409 artifact_version_conflict without writing.")}), "base_version", "entrypoint", "files")
	schemas["ArtifactProjectVersionRequest"] = object(map[string]any{"base_version": described(integer(1), "Latest version for compare-and-swap; neither restore nor share may race a newer save."), "version": described(integer(1), "Retained version to restore as a new version, or pin as the shared version.")}, "base_version", "version")
	schemas["ArtifactProjectConsentRequest"] = object(map[string]any{"run_id": str(), "page_id": str()}, "run_id")
	schemas["ArtifactProjectConsentResponse"] = object(map[string]any{"run_id": str(), "page_id": str(), "expires_at": stamp(), "authorized": boolean()}, "run_id", "page_id", "expires_at", "authorized")
	receipt := map[string]any{"page_id": str(), "slug": str(), "version": integer(1), "url": str()}
	schemas["ArtifactProjectCreateResponse"] = object(receipt, "page_id", "slug", "version", "url")
	schemas["ArtifactProjectMutationResponse"] = object(mergeProps(receipt, map[string]any{"actor": str()}), "page_id", "slug", "version", "url", "actor")
	schemas["ArtifactProjectShareResponse"] = object(map[string]any{"shared_version": integer(1), "url": str()}, "shared_version", "url")
	schemas["ArtifactProjectError"] = object(map[string]any{"error": str(), "code": str()}, "error")
	return schemas
}
