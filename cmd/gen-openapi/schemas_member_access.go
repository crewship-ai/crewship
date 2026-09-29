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
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"membership_id", "mode", "revision", "rights"},
		"properties": map[string]any{
			"membership_id": map[string]any{"type": "string", "minLength": 1},
			"mode":          map[string]any{"type": "string", "enum": []string{"trusted", "restricted"}},
			"revision":      map[string]any{"type": "integer", "format": "int64", "minimum": 1},
			"rights": map[string]any{"type": "array", "maxItems": 256, "uniqueItems": true, "items": map[string]any{"oneOf": []any{
				right("agent", []string{"discover", "chat", "run", "delegate"}),
				right("project", []string{"list", "read", "write", "delete"}),
			}}},
		},
	}
}
