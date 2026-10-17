package main

// memberAccessPolicySchema documents the complete CAS document, including the
// explicit empty array used to revoke all restricted resource rights.
func memberAccessPolicySchema() map[string]any {
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"membership_id", "mode", "revision", "rights"},
		"properties": map[string]any{
			"membership_id": map[string]any{"type": "string", "minLength": 1},
			"mode":          map[string]any{"type": "string", "enum": []string{"trusted", "restricted"}},
			"revision":      map[string]any{"type": "integer", "format": "int64", "minimum": 1},
			"rights":        map[string]any{"type": "array", "maxItems": 256, "uniqueItems": true, "items": ref("MemberAccessRight")},
		},
	}
	properties := schema["properties"].(map[string]any)
	properties["reissue"] = properties["rights"]
	properties["grant_details"] = map[string]any{"type": "array", "readOnly": true, "items": ref("MemberAccessGrant")}
	properties["preset_selections"] = map[string]any{"type": "array", "readOnly": true, "items": ref("AccessPresetSelection")}
	return schema
}

func memberEffectiveAccessSchema() map[string]any {
	properties := memberAccessPolicySchema()["properties"].(map[string]any)
	delete(properties, "rights")
	delete(properties, "grant_details")
	delete(properties, "reissue")
	delete(properties, "preset_selections")
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
	properties := map[string]any{"can_read": map[string]any{"type": "boolean"}, "can_contribute": map[string]any{"type": "boolean"}, "can_delete_issues": map[string]any{"type": "boolean"}}
	for _, key := range []string{"id", "identifier", "title", "status", "project_id", "project_name", "description"} {
		properties[key] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "identifier", "title", "status", "project_id", "project_name", "can_read", "can_contribute", "can_delete_issues"}, "properties": properties}
}

func memberAccessRightVariant(kind string, operations []string) map[string]any {
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

func memberAccessRightSchema() map[string]any {
	return map[string]any{"oneOf": []any{ref("MemberAccessAgentRight"), ref("MemberAccessProjectRight"), ref("MemberAccessRoutineRight")}}
}
func memberAccessSchemaComponents() map[string]any {
	return map[string]any{
		"MemberAccessAgentRight":   memberAccessRightVariant("agent", []string{"discover", "chat", "run", "delegate"}),
		"MemberAccessProjectRight": memberAccessRightVariant("project", []string{"list", "read", "write", "delete", "contribute", "manage", "delete_issues"}),
		"MemberAccessRoutineRight": memberAccessRightVariant("routine", []string{"view", "results", "run", "edit"}),
		"MemberAccessRight":        memberAccessRightSchema(),
		"MemberAccessGrant":        memberAccessGrantSchema(),
	}
}

func memberAccessGrantSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "id", "operation", "issuer_id", "created_at", "live"}, "properties": map[string]any{"kind": scalar("string"), "id": scalar("string"), "operation": scalar("string"), "issuer_id": scalar("string"), "issuer_name": scalar("string"), "created_at": scalar("string"), "live": scalar("boolean")}}
}
