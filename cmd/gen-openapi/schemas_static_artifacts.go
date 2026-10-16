package main

func staticArtifactSummarySchema() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	props := map[string]any{}
	for _, key := range []string{"author_user_id", "author_name", "author_run_id", "author_agent_id", "author_agent_name", "published_at", "last_activity_at"} {
		props[key] = str()
	}
	props["head_rev"] = integer()
	return map[string]any{"type": "object", "properties": props, "required": []string{"head_rev", "author_user_id", "author_name", "published_at", "last_activity_at"}}
}
func staticArtifactsSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	arr := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
	strictObj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}

	file := strictObj(map[string]any{"path": str(), "mime": str(), "size_bytes": integer(), "sha256": str()}, "path", "mime", "size_bytes", "sha256")
	manifest := strictObj(map[string]any{"format_version": integer(), "runtime_profile": str(), "entrypoint": str(), "files": arr(file), "source": map[string]any{"nullable": true}}, "format_version", "runtime_profile", "entrypoint", "files", "source")
	manifest["nullable"] = true
	revision := strictObj(map[string]any{"revision": integer(), "created_at": str(), "author_user_id": str(), "author_agent_id": str(), "author_run_id": str(), "restored_from": integer()}, "revision", "created_at", "author_user_id")
	response := strictObj(map[string]any{"page_id": str(), "head_rev": integer(), "revision": integer(), "html": str(), "manifest": manifest, "manifest_status": str(), "revisions": arr(revision), "can_write": boolean()}, "page_id", "head_rev", "revision", "html", "manifest", "manifest_status", "revisions", "can_write")
	receipt := strictObj(map[string]any{"page_id": str(), "revision": integer(), "head_rev": integer()}, "page_id", "revision", "head_rev")
	body := func(restore bool) map[string]any {
		props := map[string]any{"page_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_-]+$"}, "base_rev": map[string]any{"type": "integer", "minimum": 0}, "mutation_id": str(), "format_version": map[string]any{"type": "integer", "enum": []int{1}}, "runtime_profile": map[string]any{"type": "string", "enum": []string{"static-html-v1"}}}
		required := []string{"page_id", "base_rev", "mutation_id"}
		if restore {
			props["revision"] = map[string]any{"type": "integer", "minimum": 1}
			props["html"] = map[string]any{"type": "string", "enum": []string{""}}
			required = append(required, "revision")
		} else {
			props["html"] = str()
			props["revision"] = integer()
			required = append(required, "html")
		}
		return strictObj(props, required...)
	}
	return map[string]DomainSchema{
		"GET /api/v1/pages/{slug}/artifact":            {Response: response, Parameters: []map[string]any{{"in": "query", "name": "page_id", "required": true, "schema": str()}, {"in": "query", "name": "revision", "schema": integer()}}},
		"POST /api/v1/pages/{slug}/artifact/revisions": {Request: body(false), RequestRequired: true, Response: receipt, SuccessStatuses: []string{"200", "201"}},
		"POST /api/v1/pages/{slug}/artifact/restore":   {Request: body(true), RequestRequired: true, Response: receipt, SuccessStatuses: []string{"200", "201"}},
	}
}
