package main

func pageManagementPermissionsSchema() map[string]any {
	props := map[string]any{}
	for _, name := range []string{"read", "comment", "edit", "produce"} {
		props[name] = map[string]any{"type": "boolean"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": []string{"read", "comment", "edit", "produce"}}
}

func pageManagementSchemas(subject map[string]any) (map[string]any, map[string]any, map[string]any) {
	text := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	requestVersion := map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740990}
	responseVersion := map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}
	aclVersion := map[string]any{"type": "integer", "minimum": 0}
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	request := object(map[string]any{"dry_run": boolean, "confirm": boolean, "pages_version": requestVersion, "review_token": text}, "pages_version")
	request["oneOf"] = []any{
		map[string]any{"required": []string{"dry_run"}, "properties": map[string]any{"dry_run": map[string]any{"enum": []bool{true}}, "confirm": map[string]any{"enum": []bool{false}}, "review_token": map[string]any{"enum": []string{""}}}},
		map[string]any{"required": []string{"confirm", "review_token"}, "properties": map[string]any{"dry_run": map[string]any{"enum": []bool{false}}, "confirm": map[string]any{"enum": []bool{true}}, "review_token": map[string]any{"type": "string", "minLength": 1}}},
	}
	request["description"] = "Preview with dry_run=true. Mutation requires confirm=true and the exact preview review_token. Changed content, identity, versions or authority requires another preview. Tokens confer no access rights."
	state := object(map[string]any{"archived": boolean}, "archived")
	response := object(map[string]any{
		"dry_run": boolean, "page_id": text, "page": text, "pages_version": responseVersion,
		"before": state, "after": state, "changed": boolean, "review_token": text,
		"impact": object(map[string]any{"removed_from_default_lists": boolean, "access_changed": boolean, "content_deleted": boolean}, "removed_from_default_lists", "access_changed", "content_deleted"),
	}, "dry_run", "page_id", "page", "pages_version", "before", "after", "impact", "changed")
	folder := object(map[string]any{"id": text, "slug": text, "acl_version": aclVersion}, "id", "slug", "acl_version")
	snapshot := object(map[string]any{"subjects": array(subject), "withheld": boolean}, "subjects", "withheld")
	change := object(map[string]any{"subject_type": text, "subject_id": text, "label": text, "before": pageManagementPermissionsSchema(), "after": pageManagementPermissionsSchema()}, "subject_type", "subject_id", "before", "after")
	move := object(map[string]any{
		"dry_run": boolean, "page_id": text, "page": text, "pages_version": responseVersion,
		"from_folder": map[string]any{"oneOf": []any{folder, map[string]any{"type": "null"}}}, "to_folder": folder,
		"before": snapshot, "after": snapshot, "changes": array(change), "changed": boolean, "review_token": text,
	}, "dry_run", "page_id", "page", "pages_version", "from_folder", "to_folder", "before", "after", "changes", "changed")
	return request, response, move
}
