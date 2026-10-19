package main

// Only the confirmed empty Board format is published by the human boundary.
func boardHumanSchemaCatalog() map[string]DomainSchema {
	constant := func(kind string, value any) map[string]any { return map[string]any{"type": kind, "enum": []any{value}} }
	empty := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{}, "maxItems": 0}
	}
	state := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"page_id", "format", "seq", "records", "placements", "placement_tombstones", "can_write", "write_scope"},
		"properties": map[string]any{
			"page_id": map[string]any{"type": "string"},
			"format":  constant("string", "crewship-board-excalidraw-0.18.1-v1"),
			"seq":     map[string]any{"type": "integer", "minimum": 0, "maximum": 0},
			"records": empty(), "placements": empty(), "placement_tombstones": empty(),
			"can_write": constant("boolean", false), "write_scope": constant("string", "unavailable"),
		},
	}
	path := "/api/v1/pages/{slug}/board"
	return map[string]DomainSchema{
		"GET " + path:  {Response: state, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "409", "500"}},
		"POST " + path: {Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"mutation_id"}, "properties": map[string]any{"mutation_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Stable retry identity; initialization requires the original @PageID path reference."}}}, RequestRequired: true, Response: state, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500"}},
	}
}
