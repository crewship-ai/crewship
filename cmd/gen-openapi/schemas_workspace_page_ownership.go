package main

// workspacePageOwnershipSchemaCatalog describes the private metadata/custody
// boundary. It never models source HTML, scene JSON, specs or revision bodies.
func workspacePageOwnershipSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	nonempty := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	schemas := map[string]any{
		"WorkspacePageOwnershipSuccessor": object(map[string]any{"user_id": str(), "name": str(), "email": str()}, "user_id", "name", "email"),
		"WorkspacePageOwnershipSummary": object(map[string]any{
			"id": str(), "slug": str(), "name": str(), "type": map[string]any{"type": "string", "enum": []string{"artifact", "board"}}, "owner_user_id": str(), "owner_name": str(), "eligible_successors": array(ref("WorkspacePageOwnershipSuccessor")),
		}, "id", "slug", "name", "type", "owner_user_id", "owner_name", "eligible_successors"),
		"WorkspacePageOwnershipListResponse":    object(map[string]any{"pages": array(ref("WorkspacePageOwnershipSummary")), "next_cursor": str()}, "pages"),
		"WorkspacePageOwnershipTransferRequest": object(map[string]any{"expected_owner_user_id": nonempty(), "successor_user_id": nonempty()}, "expected_owner_user_id", "successor_user_id"),
		"WorkspacePageOwnershipError":           object(map[string]any{"error": str(), "code": map[string]any{"type": "string", "enum": []string{"owner_changed"}}}, "error"),
	}
	headers := map[string]any{"Cache-Control": map[string]any{"description": "Private custody metadata and workspace member contacts must not be cached.", "schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
	return map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/page-ownership": {
			Response: ref("WorkspacePageOwnershipListResponse"), SuccessStatuses: []string{"200"}, SuccessHeaders: headers,
			Parameters: []map[string]any{
				{"name": "owner_user_id", "in": "query", "required": false, "description": "Filter outputs by their current human owner.", "schema": str()},
				{"name": "limit", "in": "query", "required": false, "description": "Maximum output summaries per page.", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25}},
				{"name": "cursor", "in": "query", "required": false, "description": "Opaque next_cursor returned for the same workspace and owner filter.", "schema": map[string]any{"type": "string", "maxLength": 1024}},
			},
		},
		"POST /api/v1/workspaces/{workspaceId}/page-ownership/{pageID}": {
			Request: ref("WorkspacePageOwnershipTransferRequest"), RequestRequired: true, Response: ref("WorkspacePageOwnershipSummary"), SuccessStatuses: []string{"200"}, SuccessHeaders: headers,
			ErrorMedia: []string{"application/json"}, ErrorResponse: ref("WorkspacePageOwnershipError"),
		},
	}, schemas
}
