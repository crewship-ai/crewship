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
	limit := func(lo, hi int, text string) map[string]any {
		return described(map[string]any{"type": "integer", "minimum": lo, "maximum": hi}, text)
	}
	limitProps := map[string]any{
		"chat_run_create_limit":  limit(1, 20, "Artifacts one chat run may create (default 3)."),
		"run_read_limit":         limit(1, 500, "Distinct artifacts one run may read through the agent's own access (default 50)."),
		"crew_hourly_page_limit": limit(1, 1000, "Pages one crew may create per hour through the sidecar save path (default 20)."),
	}
	schemas := map[string]any{
		"WorkspaceArtifactCreationPolicyResponse": object(map[string]any{
			"enabled":  described(map[string]any{"type": "boolean"}, "Whether agents of the listed crews may create artifacts for their human."),
			"crew_ids": described(crews, "Active crews of this workspace that may create artifacts."),
			"version":  described(version, "Policy version; 0 until the policy is first saved. Send it back as base_version."),
			"limits": described(object(limitProps, "chat_run_create_limit", "run_read_limit", "crew_hourly_page_limit"),
				"Per-run and per-crew limits, applied with the artifact_agent_access_modes flag on."),
			"scoped_default": described(map[string]any{"type": "boolean"},
				"True while no policy was ever saved and artifact_agent_access_modes is on: chat runs of every live crew may use their artifact scope."),
		}, "enabled", "crew_ids", "version", "limits", "scoped_default"),
		"WorkspaceArtifactCreationPolicyRequest": object(map[string]any{
			"enabled":      map[string]any{"type": "boolean"},
			"crew_ids":     described(crews, "Complete replacement allowlist; every ID must be an active crew of this workspace."),
			"base_version": described(version, "Policy version the caller last read. A different current version returns 409 stale_policy."),
			"limits":       described(object(limitProps), "Optional: only the named limits change; omitted ones keep their value."),
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
