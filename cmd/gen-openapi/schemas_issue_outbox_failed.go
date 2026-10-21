package main

func issueOutboxFailedSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	job := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "kind", "attempts", "max_attempts", "created_at", "failed_at", "error_code"}, "properties": map[string]any{
		"id": str(), "kind": map[string]any{"type": "string", "enum": []string{"journal", "mention"}}, "attempts": map[string]any{"type": "integer", "minimum": 0}, "max_attempts": map[string]any{"type": "integer", "minimum": 1}, "created_at": str(), "failed_at": map[string]any{"type": "string", "nullable": true}, "error_code": map[string]any{"type": "string", "enum": []string{"invalid_payload", "delivery_exhausted", "authority_revoked", "unknown"}},
	}}
	page := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"jobs", "next_cursor", "has_more"}, "properties": map[string]any{"jobs": map[string]any{"type": "array", "items": ref("FailedIssueMutationJob")}, "next_cursor": str(), "has_more": map[string]any{"type": "boolean"}}}
	route := DomainSchema{Response: ref("FailedIssueMutationPage"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "403", "503"}, SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": str()}}, Parameters: []map[string]any{
		{"name": "workspaceId", "in": "path", "required": true, "schema": str()},
		{"name": "state", "in": "query", "schema": map[string]any{"type": "string", "enum": []string{"failed"}}},
		{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}},
		{"name": "cursor", "in": "query", "schema": str()},
	}}
	return map[string]DomainSchema{"GET /api/v1/workspaces/{workspaceId}/issue-mutation-outbox": route}, map[string]any{"FailedIssueMutationJob": job, "FailedIssueMutationPage": page}
}
