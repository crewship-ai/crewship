package main

// memberAccessPolicySchema documents the complete CAS document, including the
// explicit empty array used to revoke all restricted resource rights.
func memberAccessPolicySchema() map[string]any {
	right := func(kind string, operations []string) map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"kind", "id", "operation"},
			"properties": map[string]any{
				"kind":      map[string]any{"type": "string", "enum": []string{kind}},
				"id":        map[string]any{"type": "string", "minLength": 1},
				"operation": map[string]any{"type": "string", "enum": operations},
			},
		}
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"membership_id", "mode", "revision", "rights"},
		"properties": map[string]any{
			"membership_id": map[string]any{"type": "string", "minLength": 1},
			"mode":          map[string]any{"type": "string", "enum": []string{"trusted", "restricted"}},
			"revision":      map[string]any{"type": "integer", "format": "int64", "minimum": 1},
			"rights": map[string]any{"type": "array", "maxItems": 256, "uniqueItems": true, "items": map[string]any{"oneOf": []any{
				right("agent", []string{"discover", "chat", "run", "delegate"}),
				right("project", []string{"list", "read", "write", "delete", "contribute", "manage"}),
				right("routine", []string{"view", "run"}),
			}}},
		},
	}
	properties := schema["properties"].(map[string]any)
	properties["reissue"] = properties["rights"]
	properties["grant_details"] = map[string]any{"type": "array", "readOnly": true, "items": map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"kind", "id", "operation", "issuer_id", "created_at", "live"},
		"properties": map[string]any{"kind": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "operation": map[string]any{"type": "string"}, "issuer_id": map[string]any{"type": "string"}, "issuer_name": map[string]any{"type": "string"}, "created_at": map[string]any{"type": "string"}, "live": map[string]any{"type": "boolean"}},
	}}
	return schema
}

func memberEffectiveAccessSchema() map[string]any {
	properties := memberAccessPolicySchema()["properties"].(map[string]any)
	delete(properties, "rights")
	delete(properties, "grant_details")
	delete(properties, "reissue")
	properties["account_restricted"] = map[string]any{"type": "boolean"}
	properties["restriction_scope"] = map[string]any{"type": "string", "enum": []string{"account"}}
	properties["explanation"] = map[string]any{"type": "string"}
	properties["decisions"] = map[string]any{"type": "array", "items": map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"kind", "id", "operation", "state", "reason"},
		"properties": map[string]any{"name": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"agent", "project", "routine"}}, "id": map[string]any{"type": "string"}, "operation": map[string]any{"type": "string"}, "state": map[string]any{"type": "string", "enum": []string{"allowed", "denied", "conditional", "unavailable"}}, "reason": map[string]any{"type": "string"}},
	}}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"membership_id", "mode", "revision", "account_restricted", "restriction_scope", "explanation", "decisions"}, "properties": properties}
}

func restrictedIssueSchema() map[string]any {
	properties := map[string]any{"can_read": map[string]any{"type": "boolean"}, "can_contribute": map[string]any{"type": "boolean"}}
	for _, key := range []string{"id", "identifier", "title", "status", "project_id", "project_name", "description"} {
		properties[key] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "identifier", "title", "status", "project_id", "project_name", "can_read", "can_contribute"}, "properties": properties}
}
