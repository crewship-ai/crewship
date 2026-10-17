package main

// Access presets expose explicit versioned bundles; digests confer no authority.
func accessPresetSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	obj := func(p map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p, "required": required}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	nonempty := func() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	positive := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1} }
	strings := func() map[string]any { return array(str()) }
	components := map[string]any{
		"AccessPresetLevel":        obj(map[string]any{"level": nonempty(), "operations": strings()}, "level", "operations"),
		"AccessPresetDefinition":   obj(map[string]any{"preset_id": nonempty(), "version": positive(), "kind": enum("project", "routine"), "managed_operations": strings(), "levels": array(ref("AccessPresetLevel"))}, "preset_id", "version", "kind", "managed_operations", "levels"),
		"AccessPresetCatalogEntry": obj(map[string]any{"definition": ref("AccessPresetDefinition"), "definition_hash": nonempty()}, "definition", "definition_hash"),
		"AccessPresetCatalog":      obj(map[string]any{"presets": array(ref("AccessPresetCatalogEntry"))}, "presets"),
	}
	headers := map[string]any{"Cache-Control": map[string]any{"schema": enum("no-store")}}
	base := "/api/v1/workspaces/{workspaceId}"
	return map[string]DomainSchema{
		"GET " + base + "/access/presets": {Response: ref("AccessPresetCatalog"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500"}, SuccessHeaders: headers},
	}, components
}
