package main

// Explicit versioned Docs contracts; neither the document tree nor request lifecycle
// falls back to an unspecified object. Engine authorization remains independent.
func documentSchemaComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(props map[string]any, required ...string) map[string]any {
		value := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			value["required"] = required
		}
		return value
	}
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	block := obj(map[string]any{"id": str(), "hash": str()}, "id", "hash")
	outline := obj(map[string]any{"id": str(), "hash": str(), "type": str(), "text": str(), "parent": map[string]any{"type": "string", "nullable": true}}, "id", "hash", "type", "text", "parent")
	attrs := obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,80}$"}, "level": map[string]any{"type": "integer", "enum": []int{1, 2, 3}}, "start": integer(), "type": map[string]any{"type": "string", "nullable": true}, "colspan": integer(), "rowspan": integer(), "colwidth": map[string]any{"type": "array", "nullable": true, "items": integer()}, "align": map[string]any{"type": "string", "nullable": true}})
	node := obj(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"doc", "text", "paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader", "codeBlock"}}, "attrs": attrs, "content": arr(ref("DocumentNode")), "text": str(), "marks": map[string]any{"type": "array", "maxItems": 1, "items": map[string]any{"oneOf": []any{map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type", "attrs"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"link"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"href"}, "properties": map[string]any{"href": map[string]any{"type": "string", "maxLength": 2048, "description": "Canonical HTTP(S) URL without credentials or control/space characters; engine enforces UTF8 byte bound2048."}}}}}, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"code"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "maxProperties": 0}}}}}}}, "type")
	node["description"] = "Versioned bounded tree: depth<=40, nodes<=10000, stable unique block IDs and headings levels1..3. Schema1/2 forbid marks/code blocks; explicit3 adds closed mutually exclusive link/code marks and plain codeBlock. Images remain unsupported. Structural grammar and retained Yjs validation are enforced by the document engine."
	receipt := obj(map[string]any{"id": str(), "rev": integer(), "epoch": str(), "actor": str(), "kind": str(), "schema": integer(), "checkpoint": str(), "duplicate": boolean(), "found": boolean()}, "id", "rev", "epoch")
	state := obj(map[string]any{"page_id": str(), "schema": integer(), "rev": integer(), "epoch": str(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block), "content": ref("DocumentNode"), "outline": arr(outline), "can_write": boolean(), "can_comment": boolean(), "can_resolve": boolean()}, "page_id", "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks", "content", "outline", "can_write", "can_comment", "can_resolve")
	result := obj(map[string]any{"page_id": str(), "type": map[string]any{"type": "string", "enum": []string{"document"}}, "page_slug": str(), "title": str(), "revision": map[string]any{"type": "integer", "enum": []int{0}}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "agent_id": str(), "agent_name": str(), "run_id": str(), "on_behalf_of_user_id": str()}, "page_id", "type", "page_slug", "title", "revision", "created_at", "agent_id", "agent_name", "run_id", "on_behalf_of_user_id")
	return map[string]any{
		"DocumentChatResults":         obj(map[string]any{"results": map[string]any{"type": "array", "maxItems": 20, "items": result}}, "results"),
		"DocumentAvailability":        obj(map[string]any{"enabled": boolean()}, "enabled"),
		"DocumentInitializationState": obj(map[string]any{"schema": integer(), "rev": integer(), "epoch": integer(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block)}, "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks"),
		"DocumentInitializeReceipt":   obj(map[string]any{"id": str(), "rev": integer(), "epoch": integer(), "actor": str(), "kind": str(), "found": boolean(), "duplicate": boolean()}, "id", "rev", "epoch", "actor", "kind", "found", "duplicate"),
		"DocumentNode":                node, "DocumentState": state, "DocumentReceipt": receipt,
	}
}
func documentSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	body := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{"id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}, "schema": map[string]any{"type": "integer", "enum": []int{1, 2, 3}}, "epoch": str(), "page_id": str()}
		for k, v := range extra {
			props[k] = v
		}
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"id", "schema", "epoch"}, required...), "additionalProperties": false, "allOf": []any{map[string]any{"oneOf": []any{map[string]any{"properties": map[string]any{"schema": map[string]any{"enum": []int{1, 2}}}}, map[string]any{"properties": map[string]any{"schema": map[string]any{"enum": []int{3}}}, "required": []string{"page_id"}}}}}}
	}
	base := "/api/v1/pages/{slug}/document"
	out := map[string]DomainSchema{}
	get := func(path, name string) {
		out["GET "+path] = DomainSchema{Response: ref(name), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "429", "500", "502", "503"}}
	}
	get(base, "DocumentState")
	out["GET /api/v1/chats/{chatId}/artifact-results"] = DomainSchema{Response: ref("DocumentChatResults"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "500", "503"}}
	out["GET /api/v1/documents/status"] = DomainSchema{Response: ref("DocumentAvailability"), SuccessStatuses: []string{"200"}}
	post := func(path string, request map[string]any) {
		out["POST "+base+path] = DomainSchema{Request: request, RequestRequired: true, Response: ref("DocumentReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "429", "500", "502", "503"}}
	}
	initialize := body(map[string]any{"body": ref("DocumentNode")}, "body")
	initialize["required"] = []string{"id", "schema", "body"}
	delete(initialize["properties"].(map[string]any), "epoch")
	initialize["properties"].(map[string]any)["schema"] = map[string]any{"type": "integer", "enum": []int{2, 3}}
	post("/initialize", initialize)
	initSchema := out["POST "+base+"/initialize"]
	initSchema.Response = map[string]any{"oneOf": []any{ref("DocumentInitializationState"), ref("DocumentInitializeReceipt")}}
	out["POST "+base+"/initialize"] = initSchema
	human := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"human"}}, "update": map[string]any{"type": "string", "maxLength": 1400000, "description": "Base64 incremental Yjs update; agent raw updates forbidden."}}, "kind", "update")
	semantic := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"agent"}}, "target": str(), "text": str(), "ifRev": integer(), "ifHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "target", "text", "ifRev", "ifHash")
	post("/patch", map[string]any{"oneOf": []any{human, semantic}})
	for key, schema := range out {
		if len(key) > 5 && key[:5] == "POST " {
			schema.ErrorMedia = []string{"application/json"}
			schema.ErrorResponse = map[string]any{"oneOf": []any{errorSchema(), problemSchema()}}
			out[key] = schema
		}
	}
	return out
}
