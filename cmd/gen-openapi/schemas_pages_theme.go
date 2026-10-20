package main

func pagesThemeSchema() map[string]any {
	properties := map[string]any{}
	for _, key := range []string{"accent", "background", "surface", "text", "muted", "border"} {
		properties[key] = map[string]any{"type": "string", "pattern": "^#[0-9a-fA-F]{6}$"}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "description": "Workspace colors for custom Pages. Omitted keys inherit SDK defaults; an empty object resets the palette."}
}

func appearanceDefaultSchema() map[string]any {
	id := map[string]any{"type": "string", "pattern": "^[a-z0-9-]{1,32}$"}
	return map[string]any{
		"type": "object", "additionalProperties": false, "maxProperties": 4,
		"properties": map[string]any{
			"style":  id,
			"accent": id,
			"theme":  map[string]any{"type": "string", "enum": []string{"dark", "light", "system"}},
			"knobs": map[string]any{
				"type": "object", "maxProperties": 16,
				"additionalProperties": map[string]any{"type": "string", "maxLength": 64},
			},
		},
		"description": "Workspace default look (OWNER/ADMIN), at most 2 KiB. Members inherit each value they have not chosen themselves. An empty object resets it.",
	}
}
