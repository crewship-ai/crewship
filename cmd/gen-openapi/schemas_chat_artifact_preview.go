package main

func chatArtifactPreviewSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	words := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"} }
	counter := func() map[string]any { return map[string]any{"type": "integer", "format": "int64", "minimum": 0} }
	components := map[string]any{
		"ChatArtifactPreviewExpected": object(map[string]any{
			"schema": map[string]any{"type": "integer", "enum": []int{3}}, "rev": counter(), "generation": counter(),
		}, "schema", "rev", "generation"),
		"ChatArtifactPreviewChat": object(map[string]any{
			"id": map[string]any{"type": "string"}, "visibility": words("private"),
			"audience": words("author_and_workspace_operators", "author_and_workspace_owners"),
		}, "id", "visibility", "audience"),
		"ChatArtifactPreviewTarget": object(map[string]any{
			"page_id": id(), "type": words("document"), "audience": words("author_and_workspace_owners"),
			"expected": ref("ChatArtifactPreviewExpected"),
		}, "page_id", "type", "audience", "expected"),
		"ChatArtifactPreview": object(map[string]any{
			"chat": ref("ChatArtifactPreviewChat"), "target": ref("ChatArtifactPreviewTarget"),
			"can_update": map[string]any{"type": "boolean", "enum": []bool{false}}, "reason": words("targeted_iteration_unavailable"),
		}, "chat", "target", "can_update", "reason"),
	}
	return map[string]DomainSchema{"GET /api/v1/chats/{chatId}/artifact-context/preview": {
		Response: ref("ChatArtifactPreview"), SuccessStatuses: []string{"200"},
		ErrorStatuses: []string{"400", "401", "403", "404", "500", "503"}, ErrorResponse: errorSchema(),
		Parameters: []map[string]any{{"name": "page_id", "in": "query", "required": true, "schema": id(), "description": "Exactly one immutable Page ID; slugs are not resolved."}},
		SuccessHeaders: map[string]any{
			"Cache-Control":          map[string]any{"schema": words("no-store")},
			"X-Content-Type-Options": map[string]any{"schema": words("nosniff")},
		},
	}}, components
}
