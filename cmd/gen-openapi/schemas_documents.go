package main

// Explicit schema-2 contracts; neither the document tree nor request lifecycle
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
	node := obj(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"doc", "text", "paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader"}}, "attrs": attrs, "content": arr(ref("DocumentNode")), "text": str(), "marks": map[string]any{"type": "array", "maxItems": 0, "items": map[string]any{"type": "string"}}}, "type")
	node["description"] = "Schema-2 bounded tree: depth <=40 and <=10000 nodes; stable unique IDs on blocks, headings levels1..3, no marks. Structural grammar and retained Yjs validation are enforced by the document engine."
	receipt := obj(map[string]any{"id": str(), "rev": integer(), "epoch": str(), "actor": str(), "kind": str(), "schema": integer(), "checkpoint": str(), "duplicate": boolean(), "found": boolean()}, "id", "rev", "epoch")
	state := obj(map[string]any{"page_id": str(), "schema": integer(), "rev": integer(), "epoch": str(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block), "content": ref("DocumentNode"), "outline": arr(outline), "can_write": boolean(), "can_comment": boolean(), "can_resolve": boolean()}, "page_id", "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks", "content", "outline", "can_write", "can_comment", "can_resolve")
	checkpoint := obj(map[string]any{"id": str(), "rev": integer(), "schema": integer(), "actor": str(), "created_at": str(), "snapshot": str(), "blocks": arr(block), "content": ref("DocumentNode"), "outline": arr(outline)}, "id", "rev", "schema")
	anchor := obj(map[string]any{"id": str(), "block": str(), "quote": str(), "checkpoint": integer(), "start": str(), "end": str()}, "id", "block", "quote", "checkpoint", "start", "end")
	comment := obj(map[string]any{"id": str(), "body": str(), "anchor": anchor, "actor": str(), "rev": integer(), "version": integer(), "resolved": boolean(), "created_at": str(), "block": str(), "quote": str(), "checkpoint": integer(), "start": str(), "end": str(), "status": map[string]any{"type": "string", "enum": []string{"attached", "collapsed", "detached", "resolved"}}, "from": integer(), "to": integer()}, "id", "body", "anchor", "actor", "rev", "version", "resolved", "created_at", "block", "quote", "checkpoint", "start", "end", "status")
	job := obj(map[string]any{"id": str(), "status": map[string]any{"type": "string", "enum": []string{"queued", "running", "succeeded", "failed"}}, "run_id": str(), "result_revision": integer(), "checkpoint_id": str(), "error": str(), "created_at": str(), "updated_at": str(), "instruction": str(), "base_rev": integer()}, "id", "status", "run_id", "result_revision", "checkpoint_id", "error", "created_at", "updated_at", "instruction", "base_rev")
	target := obj(map[string]any{"chat_id": str(), "title": str(), "agent_id": str(), "agent_name": str(), "agent_slug": str()}, "chat_id", "title", "agent_id", "agent_name", "agent_slug")
	return map[string]any{"DocumentInitializationState": obj(map[string]any{"schema": integer(), "rev": integer(), "epoch": integer(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block)}, "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks"), "DocumentInitializeReceipt": obj(map[string]any{"id": str(), "rev": integer(), "epoch": integer(), "actor": str(), "kind": str(), "found": boolean(), "duplicate": boolean()}, "id", "rev", "epoch", "actor", "kind", "found", "duplicate"), "DocumentNode": node, "DocumentState": state, "DocumentReceipt": receipt, "DocumentCheckpoint": checkpoint, "DocumentComment": comment, "DocumentComments": obj(map[string]any{"comments": arr(ref("DocumentComment")), "can_write": boolean(), "can_comment": boolean(), "can_resolve": boolean()}, "comments", "can_write", "can_comment", "can_resolve"), "DocumentCheckpoints": obj(map[string]any{"checkpoints": arr(ref("DocumentCheckpoint"))}, "checkpoints"), "DocumentRequests": obj(map[string]any{"requests": arr(job), "can_dispatch": boolean(), "eligible_chats": arr(target)}, "requests", "can_dispatch", "eligible_chats")}
}
func documentSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	body := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{"id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}, "schema": map[string]any{"type": "integer", "enum": []int{2}}, "epoch": str()}
		for k, v := range extra {
			props[k] = v
		}
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"id", "schema", "epoch"}, required...), "additionalProperties": false}
	}
	base := "/api/v1/pages/{slug}/document"
	out := map[string]DomainSchema{}
	get := func(path, name string) {
		out["GET "+path] = DomainSchema{Response: ref(name), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500", "502", "503"}}
	}
	get(base, "DocumentState")
	get(base+"/checkpoints", "DocumentCheckpoints")
	get(base+"/checkpoints/{checkpoint}", "DocumentCheckpoint")
	get(base+"/comments", "DocumentComments")
	get(base+"/requests", "DocumentRequests")
	out["GET /api/v1/documents/status"] = DomainSchema{Response: map[string]any{"type": "object", "required": []string{"enabled"}, "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}}, SuccessStatuses: []string{"200"}}
	out["GET "+base+"/export"] = DomainSchema{Response: map[string]any{"type": "string", "description": "Markdown of the currently confirmed document."}, ResponseMedia: []string{"text/markdown"}, SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "500", "503"}}
	post := func(path string, request map[string]any) {
		out["POST "+base+path] = DomainSchema{Request: request, RequestRequired: true, Response: ref("DocumentReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500", "502", "503"}}
	}
	initialize := body(map[string]any{"body": ref("DocumentNode")}, "body")
	initialize["required"] = []string{"id", "schema", "body"}
	delete(initialize["properties"].(map[string]any), "epoch")
	post("/initialize", initialize)
	initSchema := out["POST "+base+"/initialize"]
	initSchema.Response = map[string]any{"oneOf": []any{ref("DocumentInitializationState"), ref("DocumentInitializeReceipt")}}
	out["POST "+base+"/initialize"] = initSchema
	human := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"human"}}, "update": map[string]any{"type": "string", "maxLength": 1400000, "description": "Base64 incremental Yjs update; agent raw updates forbidden."}}, "kind", "update")
	semantic := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"agent"}}, "target": str(), "text": str(), "ifRev": integer(), "ifHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "target", "text", "ifRev", "ifHash")
	post("/patch", map[string]any{"oneOf": []any{human, semantic}})
	post("/checkpoint", body(map[string]any{"ifRev": integer()}, "ifRev"))
	post("/restore", body(map[string]any{"ifRev": integer(), "checkpoint": str()}, "ifRev", "checkpoint"))
	post("/comments", body(map[string]any{"ifRev": integer(), "block": str(), "from": integer(), "to": integer(), "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 10000, "description": "Nonempty comment text, at most 10000 UTF-8 bytes (the byte bound also applies to multibyte text)."}}, "ifRev", "block", "from", "to", "text"))
	post("/comments/resolve", body(map[string]any{"ifRev": integer(), "comment": str(), "version": map[string]any{"type": "integer", "minimum": 1}, "resolved": map[string]any{"type": "boolean"}}, "ifRev", "comment", "version", "resolved"))
	out["POST "+base+"/requests"] = DomainSchema{RequestRequired: true, Request: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"page_id", "chat_id", "instruction", "base_rev", "submission_id", "targets"}, "properties": map[string]any{"page_id": str(), "targets": map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "description": "Immutable paragraph/heading selections; total quotes at most4096 UTF-16 code units. Original base and hashes must remain valid; no automatic rebase. Current completion supports one textblock and one patch.", "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "hash", "from", "to", "quote"}, "properties": map[string]any{"id": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "hash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "from": integer(), "to": integer(), "quote": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096, "description": "Exact nonempty substring at UTF-16 offsets [from,to); aggregate UTF-16 limit4096 enforced by server."}}}}, "chat_id": str(), "instruction": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}, "base_rev": integer(), "submission_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}}, Response: map[string]any{"type": "object", "required": []string{"id", "request_id"}, "properties": map[string]any{"id": str(), "request_id": str()}}, SuccessStatuses: []string{"202"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500", "503"}}
	return out
}
