package main

func artifactCreationPolicySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	version := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	crews := map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "maxItems": 500, "uniqueItems": true}
	schemas := map[string]any{
		"WorkspaceArtifactCreationPolicyResponse": object(map[string]any{
			"enabled": map[string]any{"type": "boolean"}, "crew_ids": crews, "version": version,
		}, "enabled", "crew_ids", "version"),
		"WorkspaceArtifactCreationPolicyRequest": object(map[string]any{
			"enabled": map[string]any{"type": "boolean"}, "crew_ids": crews, "base_version": version,
		}, "enabled", "crew_ids", "base_version"),
		"WorkspaceArtifactCreationPolicyError": object(map[string]any{
			"error": map[string]any{"type": "string"}, "code": map[string]any{"type": "string", "enum": []string{"stale_policy"}},
		}, "error"),
	}
	headers := map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
	routes := map[string]DomainSchema{}
	for _, method := range []string{"GET", "PUT"} {
		route := DomainSchema{
			Response: ref("WorkspaceArtifactCreationPolicyResponse"), SuccessStatuses: []string{"200"}, SuccessHeaders: headers,
			ErrorMedia: []string{"application/json"}, ErrorResponse: ref("WorkspaceArtifactCreationPolicyError"),
		}
		if method == "PUT" {
			route.Request = ref("WorkspaceArtifactCreationPolicyRequest")
			route.RequestRequired = true
		}
		routes[method+" /api/v1/workspaces/{workspaceId}/artifact-creation-policy"] = route
	}
	return routes, schemas
}
