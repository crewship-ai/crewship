package main

// These resources are immutable versions, not paths in legacy crew storage.
func restrictedProjectFileSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	version := func() map[string]any {
		return obj(map[string]any{
			"version_id": str(), "file_id": str(), "project_id": str(), "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 240},
			"revision":   map[string]any{"type": "integer", "format": "int64", "minimum": 1},
			"size_bytes": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 1048576},
			"sha256":     map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"},
			"created_at": map[string]any{"type": "string", "format": "date-time"},
		}, "version_id", "file_id", "project_id", "name", "revision", "size_bytes", "sha256", "created_at")
	}
	list := obj(map[string]any{"files": map[string]any{"type": "array", "maxItems": 256, "items": version()}}, "files")
	option := version()
	option["properties"].(map[string]any)["project_name"] = str()
	option["required"] = append(option["required"].([]string), "project_name")
	options := obj(map[string]any{"files": map[string]any{"type": "array", "maxItems": 100, "items": option}, "has_more": map[string]any{"type": "boolean"}}, "files", "has_more")
	upload := obj(map[string]any{
		"file_id":           map[string]any{"type": "string", "description": "Omit to create; supply the current file ID to replace."},
		"name":              map[string]any{"type": "string", "minLength": 1, "maxLength": 240, "description": "Portable relative filename, at most 240 UTF-8 bytes; unchanged on replacement."},
		"expected_revision": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "default": 0, "description": "Zero creates; a replacement must match the current positive revision."},
		"content_base64":    map[string]any{"type": "string", "format": "byte", "nullable": true, "maxLength": 1398104, "description": "Standard base64, at most 1 MiB decoded. Omitted, null or empty string creates empty content. Aggregate project/workspace capacity is checked atomically."},
	}, "name")
	retire := obj(map[string]any{"expected_revision": map[string]any{"type": "integer", "format": "int64", "minimum": 1}}, "expected_revision")
	errorBody := obj(map[string]any{"error": str()}, "error")
	base := "/api/v1/workspaces/{workspaceId}/projects/{projectId}/files"
	catalog := map[string]DomainSchema{
		"GET " + base:                           {Response: list, SuccessStatuses: []string{"200"}},
		"POST " + base:                          {RequestRequired: true, Request: upload, Response: version(), SuccessStatuses: []string{"201"}},
		"DELETE " + base + "/{fileId}":          {RequestRequired: true, Request: retire, SuccessStatuses: []string{"204"}},
		"GET " + base + "/{versionId}/download": {Response: map[string]any{"type": "string", "format": "binary"}, ResponseMedia: []string{"application/octet-stream"}, SuccessStatuses: []string{"200"}, SuccessHeaders: map[string]any{"Content-Length": map[string]any{"schema": map[string]any{"type": "integer", "format": "int64", "minimum": 0, "maximum": 1048576}, "description": "Exact decoded length; interrupted delivery is incomplete, never a successful prefix."}, "X-Content-SHA256": map[string]any{"schema": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, "description": "Immutable exact version hash."}, "Content-Disposition": map[string]any{"schema": str(), "description": "Attachment with the source basename."}, "X-Content-Type-Options": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"nosniff"}}}}},
		"GET /api/v1/chats/{chatId}/project-input-options": {Response: options, SuccessStatuses: []string{"200"}, Parameters: []map[string]any{
			{"name": "workspace_id", "in": "query", "required": true, "schema": str()},
			{"name": "search", "in": "query", "required": false, "schema": map[string]any{"type": "string", "maxLength": 128, "description": "Filename/project substring, at most 128 UTF-8 bytes."}},
		}},
	}
	for key, contract := range catalog {
		contract.ErrorMedia = []string{"application/json"}
		contract.ErrorResponse = errorBody
		catalog[key] = contract
	}
	return catalog
}
