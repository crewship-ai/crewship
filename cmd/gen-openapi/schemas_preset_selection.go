package main

func presetSelectionSchemaComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	return map[string]any{"AccessPresetSelection": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "kind", "resource_id", "preset_id", "version", "level", "definition_hash", "selected_at", "state"}, "properties": map[string]any{
		"id": str(), "kind": map[string]any{"type": "string", "enum": []string{"project", "routine"}}, "resource_id": str(), "preset_id": str(), "version": map[string]any{"type": "integer", "minimum": 1}, "level": str(), "definition_hash": str(), "selected_by": str(), "selected_at": str(), "state": map[string]any{"type": "string", "enum": []string{"selected", "custom"}},
	}}}
}
