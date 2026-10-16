package main

func documentBlockReadSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required":    []string{"page_id", "schema", "rev", "generation", "epoch", "block"},
		"description": "Current text-block projection selected by block_id. Rechecks the original Page, exact caller authority and entire persisted source after managed read; source changes return409. Full block text is capped at256KiB UTF8 and never silently truncated; the existing complete managed read envelope must fit4MiB or returns502. No arbitrary history or new agent authority.",
		"properties": map[string]any{
			"page_id":    map[string]any{"type": "string"},
			"schema":     map[string]any{"type": "integer", "enum": []int{1, 2, 3}},
			"rev":        map[string]any{"type": "integer", "minimum": 0},
			"generation": map[string]any{"type": "integer", "minimum": 0},
			"epoch":      map[string]any{"type": "string"},
			"block": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "type", "parent", "hash", "text"}, "properties": map[string]any{
				"id":     map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,80}$"},
				"type":   map[string]any{"type": "string", "enum": []string{"paragraph", "heading", "codeBlock"}},
				"parent": map[string]any{"type": "string", "nullable": true},
				"hash":   map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"},
				"text":   map[string]any{"type": "string", "maxLength": 262144, "description": "Complete plaintext, at most256KiB UTF8 bytes, independently of Unicode character count. Hash binds full canonical block including structure/marks, not a summarized text snippet."},
			}},
		},
	}
}
