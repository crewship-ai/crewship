package sidecar

// The host still validates cross-field relationships, retained history and
// application support. This shape lets MCP clients send the observations that
// native workbook commands already accept, including an explicit empty set.
func workbookReadSetMCPSchema() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	address := map[string]any{"type": "string", "pattern": "^[A-Z]{1,3}[1-9][0-9]{0,6}$"}
	revision := map[string]any{"type": "integer", "minimum": 1}
	cell := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"sheet": text, "cell": address, "rev": revision},
		"required":   []string{"sheet", "cell", "rev"},
	}
	region := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"sheet": text, "start": address, "end": address, "rev": revision, "structure_rev": revision},
		"required":   []string{"sheet", "start", "end", "rev", "structure_rev"},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Inputs observed at document revisions. Omit only with exact-base agent semantics; {} declares an input-independent change. Preserve this entire set on retry. Range structure_rev comes from the read response; the host checks current history and range semantics.",
		"properties": map[string]any{
			"cells":  map[string]any{"type": "array", "maxItems": 256, "items": cell},
			"ranges": map[string]any{"type": "array", "maxItems": 32, "items": region},
		},
	}
}
