package main

func crewMiseLockSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	text := map[string]any{"type": "string"}
	hash := map[string]any{"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"}
	versions := map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	lock := object(map[string]any{
		"schema_version": map[string]any{"type": "integer", "enum": []int{1}},
		"files":          map[string]any{"type": "object", "minProperties": 1, "maxProperties": 128, "additionalProperties": map[string]any{"type": "string", "maxLength": 131072}, "description": "Complete UTF-8 native bundle. mise.lock is required; auxiliary paths are bounded relative .mise/locks paths. Server enforces 128 KiB per file and 512 KiB aggregate bytes, no NUL, traversal or parent/file conflicts."},
	}, "schema_version", "files")
	config := object(map[string]any{
		"tools": map[string]any{"type": "object", "minProperties": 1, "maxProperties": 20, "additionalProperties": text, "description": "Validated native mise tool selectors. No config environment, tasks or caller-selected resolver image."},
		"lock":  map[string]any{"allOf": []any{ref("CrewMiseNativeLockBundle")}, "nullable": true},
	}, "tools")
	tool := object(map[string]any{"name": text, "backend": text, "previous_versions": versions, "resolved_versions": versions, "version_changed": map[string]any{"type": "boolean"}}, "name", "backend", "previous_versions", "resolved_versions", "version_changed")
	proposal := object(map[string]any{
		"tools":            map[string]any{"type": "array", "maxItems": 128, "items": tool},
		"selectors_sha256": hash, "previous_lock_sha256": hash, "lock_sha256": hash,
		"lock_changed": map[string]any{"type": "boolean"}, "resolver_image_id": hash,
		"platform":     map[string]any{"type": "string", "enum": []string{"linux-x64", "linux-arm64"}},
		"mise_version": text, "lock": ref("CrewMiseNativeLockBundle"),
	}, "tools", "selectors_sha256", "lock_sha256", "lock_changed", "resolver_image_id", "platform", "mise_version", "lock")
	proposal["description"] = "Reviewable proposal only; hashes are input/output identities, not authorization. Does not apply config, publish a revision, replace a runtime or qualify installed tools."
	schemas := map[string]any{
		"CrewMiseNativeLockBundle":   lock,
		"CrewMiseLockResolveRequest": object(map[string]any{"config": config, "bump": map[string]any{"type": "boolean", "default": false}}, "config"),
		"CrewMiseLockProposal":       proposal,
	}
	return map[string]DomainSchema{"POST /api/v1/crews/{crewId}/provision/lock-resolve": {Request: ref("CrewMiseLockResolveRequest"), RequestRequired: true, Response: ref("CrewMiseLockProposal"), SuccessStatuses: []string{"200"}}}, schemas
}
