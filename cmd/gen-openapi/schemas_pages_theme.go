package main

func pagesThemeSchema() map[string]any {
	properties := map[string]any{}
	for _, key := range []string{"accent", "background", "surface", "text", "muted", "border"} {
		properties[key] = map[string]any{"type": "string", "pattern": "^#[0-9a-fA-F]{6}$"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "description": "Workspace colors for custom Pages. Omitted keys inherit SDK defaults; an empty object resets the palette."}
}
