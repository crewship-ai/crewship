package main

func discussionPaginationParameters(artifact bool) []map[string]any {
	params := []map[string]any{
		{"name": "status", "in": "query", "required": false, "schema": map[string]any{"type": "string", "enum": []string{"all", "open", "resolved"}, "default": "all"}},
		{"name": "limit", "in": "query", "required": false, "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100}},
		{"name": "cursor", "in": "query", "required": false, "description": "next_cursor for the same workspace, Page, kind and filters; confers no authority.", "schema": map[string]any{"type": "string", "maxLength": 4096}},
	}
	if artifact {
		params = append(params, map[string]any{"name": "revision", "in": "query", "required": false, "schema": map[string]any{"type": "integer", "minimum": 1}})
	}
	return params
}
