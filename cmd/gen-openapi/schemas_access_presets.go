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
		"AccessPresetApplyRequest": obj(map[string]any{"selection": ref("AccessPresetRequest"), "confirmation_digest": map[string]any{"type": "string", "pattern": "^[A-Fa-f0-9]{64}$"}}, "selection", "confirmation_digest"),
		"AccessPresetRequest":      obj(map[string]any{"membership_id": nonempty(), "revision": positive(), "resource": obj(map[string]any{"kind": enum("project", "routine"), "id": nonempty()}, "kind", "id"), "preset_id": nonempty(), "preset_version": positive(), "level": nonempty(), "reissue": map[string]any{"type": "array", "maxItems": 256, "uniqueItems": true, "items": ref("MemberAccessRight")}}, "membership_id", "revision", "resource", "preset_id", "preset_version", "level"),
		"AccessPresetChange":       obj(map[string]any{"right": ref("MemberAccessRight"), "action": enum("add", "remove", "retain", "reissue"), "issuer_id": nonempty(), "created_at": str(), "live": scalar("boolean")}, "right", "action", "issuer_id", "live"),
		"AccessPresetWarning":      obj(map[string]any{"code": nonempty(), "message": nonempty(), "before_eligible": scalar("boolean"), "after_eligible": scalar("boolean")}, "code", "message"),
		"AccessPresetPreview":      obj(map[string]any{"selection": ref("AccessPresetRequest"), "definition_hash": nonempty(), "current_selection": map[string]any{"allOf": []any{ref("AccessPresetSelection")}, "nullable": true}, "before": array(ref("MemberAccessGrant")), "changes": array(ref("AccessPresetChange")), "after_rights": map[string]any{"type": "array", "maxItems": 256, "items": ref("MemberAccessRight")}, "orthogonal_rights": array(ref("MemberAccessGrant")), "warnings": array(ref("AccessPresetWarning")), "confirmation_digest": nonempty()}, "selection", "definition_hash", "current_selection", "before", "changes", "after_rights", "orthogonal_rights", "warnings", "confirmation_digest"),
	}
	headers := map[string]any{"Cache-Control": map[string]any{"schema": enum("no-store")}}
	base := "/api/v1/workspaces/{workspaceId}"
	return map[string]DomainSchema{
		"GET " + base + "/access/presets":                             {Response: ref("AccessPresetCatalog"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500"}, SuccessHeaders: headers},
		"POST " + base + "/members/{memberId}/access/presets/apply":   {Request: ref("AccessPresetApplyRequest"), RequestRequired: true, Response: ref("MemberAccessPolicy"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "500"}, SuccessHeaders: headers},
		"POST " + base + "/members/{memberId}/access/presets/preview": {Request: ref("AccessPresetRequest"), RequestRequired: true, Response: ref("AccessPresetPreview"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "500"}, SuccessHeaders: headers},
	}, components
}
