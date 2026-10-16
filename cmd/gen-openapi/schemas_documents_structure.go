package main

func documentStructureOperationsSchema() map[string]any {
	closed := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	}
	kind := func(value string) map[string]any { return map[string]any{"type": "string", "enum": []string{value}} }
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,80}$"} }
	text := closed(map[string]any{"type": kind("text"), "text": map[string]any{"type": "string", "minLength": 1}}, "type", "text")
	content := map[string]any{"type": "array", "items": text}
	plain := closed(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"paragraph", "codeBlock"}}, "content": content}, "type")
	heading := closed(map[string]any{"type": kind("heading"), "content": content, "attrs": closed(map[string]any{"level": map[string]any{"type": "integer", "enum": []int{1, 2, 3}}}, "level")}, "type", "attrs")
	insert := closed(map[string]any{"op": kind("insert_after"), "after_id": id(), "node": map[string]any{"oneOf": []any{plain, heading}}}, "op", "after_id", "node")
	remove := closed(map[string]any{"op": kind("delete"), "id": id()}, "op", "id")
	move := closed(map[string]any{"op": kind("move_before"), "id": id(), "before_id": id()}, "op", "id", "before_id")
	return map[string]any{"type": "array", "minItems": 1, "maxItems": 256, "items": map[string]any{"oneOf": []any{insert, remove, move}}}
}
