package main

func artifactCreationPolicySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	described := func(schema map[string]any, text string) map[string]any {
		out := map[string]any{"description": text}
		for key, value := range schema {
			out[key] = value
		}
		return out
	}
	version := map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	crews := map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "maxItems": 500, "uniqueItems": true}
	schemas := map[string]any{
		"WorkspaceArtifactCreationPolicyResponse": object(map[string]any{
			"enabled":  described(map[string]any{"type": "boolean"}, "Whether agents of the listed crews may create artifacts for their human."),
			"crew_ids": described(crews, "Active crews of this workspace that may create artifacts."),
			"version":  described(version, "Policy version; 0 until the policy is first saved. Send it back as base_version."),
		}, "enabled", "crew_ids", "version"),
		"WorkspaceArtifactCreationPolicyRequest": object(map[string]any{
			"enabled":      map[string]any{"type": "boolean"},
			"crew_ids":     described(crews, "Complete replacement allowlist; every ID must be an active crew of this workspace."),
			"base_version": described(version, "Policy version the caller last read. A different current version returns 409 stale_policy."),
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
