package main

// These are key envelopes, never document payloads. The union preserves the
// handler's mutually exclusive operations, including accepted empty fields.
func localDraftKeySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	literal := func(value string) map[string]any { return map[string]any{"type": "string", "enum": []string{value}} }
	human := map[string]any{"type": "string", "minLength": 1, "description": "Expected original human identity; must equal the current credential."}
	key := map[string]any{"type": "string", "pattern": `^[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`, "description": "Canonical base64 encoding of exactly 32 bytes."}
	envelope := map[string]any{"type": "string", "minLength": 1, "maxLength": 2048}
	schemas := map[string]any{
		"LocalDraftKeyRequest": map[string]any{"oneOf": []any{
			object(map[string]any{"human": human, "operation": literal("wrap"), "key": key, "envelope": literal("")}, "human", "operation", "key"),
			object(map[string]any{"human": human, "operation": literal("unwrap"), "envelope": envelope, "key": literal("")}, "human", "operation", "envelope"),
		}, "description": "At most 4 KiB JSON; requires a current human credential and current read of the immutable Page."},
		"LocalDraftKeyResponse": map[string]any{"oneOf": []any{
			object(map[string]any{"envelope": envelope}, "envelope"),
			object(map[string]any{"key": key}, "key"),
		}},
	}
	routes := map[string]DomainSchema{"POST /api/v1/pages/{slug}/draft-key": {
		Request: ref("LocalDraftKeyRequest"), RequestRequired: true, Response: ref("LocalDraftKeyResponse"), SuccessStatuses: []string{"200"},
		ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "500", "503"},
		Parameters: []map[string]any{
			{"name": "slug", "in": "path", "required": true, "schema": map[string]any{"type": "string", "pattern": `^@[A-Za-z0-9_-]{1,128}$`}, "description": "Explicit immutable @Page ID; slug aliases are rejected."},
			{"name": "workspace_id", "in": "query", "required": false, "schema": map[string]any{"type": "string"}, "description": "Workspace ID or slug; the workspace header is also accepted."},
		},
		SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": literal("no-store")}},
	}}
	return routes, schemas
}
