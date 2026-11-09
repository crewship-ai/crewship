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
	attrs := obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,80}$"}, "level": map[string]any{"type": "integer", "enum": []int{1, 2, 3}}, "start": integer(), "type": map[string]any{"type": "string", "nullable": true}, "colspan": integer(), "rowspan": integer(), "colwidth": map[string]any{"type": "array", "nullable": true, "items": integer()}, "align": map[string]any{"type": "string", "nullable": true}, "language": map[string]any{"type": "string", "nullable": true, "pattern": "^[a-z0-9][a-z0-9+#-]{0,31}$", "description": "Schema 4 code block language."}, "checked": map[string]any{"type": "boolean", "description": "Schema 4 task item state."}, "value": map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$", "description": "Schema 5 date: one real calendar day, never a time."}, "userId": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$", "description": "Schema 5 mention: opaque user id only. The name is resolved per reader (GET .../document/people); content never stores names."}})
	node := obj(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"doc", "text", "paragraph", "heading", "bulletList", "orderedList", "listItem", "table", "tableRow", "tableCell", "tableHeader", "codeBlock", "blockquote", "taskList", "taskItem", "date", "mention"}}, "attrs": attrs, "content": arr(ref("DocumentNode")), "text": str(), "marks": map[string]any{"type": "array", "maxItems": 7, "items": map[string]any{"oneOf": []any{map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"bold", "italic", "strike", "underline"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "maxProperties": 0}}}, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type", "attrs"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"link"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"href"}, "properties": map[string]any{"href": map[string]any{"type": "string", "maxLength": 2048, "description": "Canonical HTTP(S) URL without credentials or control/space characters; engine enforces UTF8 byte bound2048."}}}}}, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"code"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "maxProperties": 0}}}, map[string]any{"type": "object", "additionalProperties": false, "required": []string{"type", "attrs"}, "properties": map[string]any{"type": map[string]any{"type": "string", "enum": []string{"textColor", "highlight"}}, "attrs": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"color"}, "properties": map[string]any{"color": map[string]any{"type": "string", "enum": []string{"gray", "red", "orange", "yellow", "green", "blue", "purple", "pink"}, "description": "Schema 5 palette name; never a CSS value. Each theme resolves it."}}}}}}}}}, "type")
	node["description"] = "Versioned bounded tree: depth<=40, nodes<=10000, stable unique block IDs and headings levels1..3. Schema1/2 forbid marks/code blocks; explicit3 adds closed mutually exclusive link/code marks (one per text) and plain codeBlock; explicit4 adds bold/italic/strike marks combinable with each other and link (inline code stays alone), codeBlock language, blockquote and taskList/taskItem(checked); explicit5 adds underline and textColor/highlight marks whose only attribute is a palette name (at most 7 marks per text), and inline atoms date{value: YYYY-MM-DD} and mention{userId} inside paragraphs and headings, with exactly that one attribute, no content and no marks. Images remain unsupported. Structural grammar and retained Yjs validation are enforced by the document engine."
	receipt := obj(map[string]any{"id": str(), "rev": integer(), "epoch": str(), "actor": str(), "kind": str(), "schema": integer(), "checkpoint": str(), "duplicate": boolean(), "found": boolean()}, "id", "rev", "epoch")
	state := obj(map[string]any{"page_id": str(), "schema": integer(), "rev": integer(), "epoch": str(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block), "content": ref("DocumentNode"), "outline": arr(outline), "can_write": boolean(), "can_comment": boolean(), "can_resolve": boolean(), "proposals": map[string]any{"type": "boolean", "description": "Agent proposals and their review are served on this Doc (schema 4; never for restricted members)."}}, "page_id", "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks", "content", "outline", "can_write", "can_comment", "can_resolve", "proposals")
	result := obj(map[string]any{"page_id": str(), "type": map[string]any{"type": "string", "enum": []string{"document"}}, "page_slug": str(), "title": str(), "revision": map[string]any{"type": "integer", "enum": []int{0}}, "created_at": map[string]any{"type": "string", "format": "date-time"}, "agent_id": str(), "agent_name": str(), "run_id": str(), "on_behalf_of_user_id": str()}, "page_id", "type", "page_slug", "title", "revision", "created_at", "agent_id", "agent_name", "run_id", "on_behalf_of_user_id")
	comment := obj(map[string]any{"id": str(), "parent": map[string]any{"type": "string", "description": "Root thread a reply answers; absent on a thread."}, "body": str(), "actor": str(), "rev": integer(), "version": map[string]any{"type": "integer", "minimum": 1}, "resolved": boolean(), "created_at": str(),
		"block": str(), "quote": str(), "checkpoint": integer(), "start": str(), "end": str(), "from": integer(), "to": integer(),
		"status": map[string]any{"type": "string", "enum": []string{"attached", "collapsed", "detached", "resolved"}, "description": "Threads only."},
		"anchor": obj(map[string]any{"id": str(), "block": str(), "quote": str(), "checkpoint": integer(), "start": str(), "end": str()})}, "id", "body", "actor", "rev", "version", "resolved", "created_at")
	comment["description"] = "A thread (anchored to quoted text, with status and UTF-16 from/to) or a reply (parent, no anchor). Comments never change document content."
	checkpointID := map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"}
	revision := map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740991}
	checkpoint := obj(map[string]any{"id": checkpointID, "rev": revision, "schema": map[string]any{"type": "integer", "minimum": 1}, "actor": str(), "created_at": str()}, "id", "rev", "schema", "actor", "created_at")
	checkpoint["additionalProperties"] = false
	history := obj(map[string]any{"checkpoints": map[string]any{"type": "array", "maxItems": 1000, "items": checkpoint}}, "checkpoints")
	history["additionalProperties"] = false
	selected := obj(map[string]any{"page_id": str(), "id": checkpointID, "rev": revision, "schema": map[string]any{"type": "integer", "enum": []int{1, 2, 3, 4, 5}}, "content": ref("DocumentNode")}, "page_id", "id", "rev", "schema", "content")
	selected["additionalProperties"] = false
	proposalID := map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}
	proposalProps := map[string]any{"id": proposalID, "author": str(), "run_id": str(), "target": obj(map[string]any{"block_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 1, "items": str()}}, "block_ids"),
		"base_rev": revision, "status": map[string]any{"type": "string", "enum": []string{"open", "stale", "accepted", "rejected"}, "description": "stale: the target block changed after the proposal was made; it can no longer be accepted."},
		"text": map[string]any{"type": "string", "maxLength": 100000}, "created_at": str(), "decided_by": str(), "accepted_rev": revision}
	proposalRequired := []string{"id", "author", "target", "base_rev", "status", "text", "created_at"}
	proposal := obj(proposalProps, proposalRequired...)
	detailProps := map[string]any{"base_content": ref("DocumentNode"), "proposed_content": ref("DocumentNode")}
	for k, v := range proposalProps {
		detailProps[k] = v
	}
	proposalDetail := obj(detailProps, proposalRequired...)
	proposalDetail["description"] = "One proposal with its target block as the agent saw it (base_content) and after the change (proposed_content); a single-proposal read always includes both."
	return map[string]any{
		"DocumentProposal":            proposal,
		"DocumentProposalDetail":      proposalDetail,
		"DocumentProposalList":        obj(map[string]any{"page_id": str(), "proposals": map[string]any{"type": "array", "maxItems": 500, "items": ref("DocumentProposal")}}, "page_id", "proposals"),
		"DocumentProposalReceipt":     obj(map[string]any{"page_id": str(), "proposal": proposalID, "id": proposalID, "rev": revision, "kind": map[string]any{"type": "string", "enum": []string{"human", "proposal-reject"}}, "duplicate": boolean()}, "page_id", "proposal", "id", "rev", "kind"),
		"DocumentRestoreReceipt":      obj(map[string]any{"page_id": str(), "id": checkpointID, "rev": revision, "epoch": str(), "kind": map[string]any{"type": "string", "enum": []string{"restore"}}, "schema": map[string]any{"type": "integer", "enum": []int{1, 2, 3, 4, 5}}, "duplicate": boolean()}, "page_id", "id", "rev", "epoch", "kind"),
		"DocumentCheckpointList":      history,
		"DocumentCheckpoint":          selected,
		"DocumentChatResults":         obj(map[string]any{"results": map[string]any{"type": "array", "maxItems": 20, "items": result}}, "results"),
		"DocumentAvailability":        obj(map[string]any{"enabled": boolean(), "wordcraft_native": boolean(), "gridcraft_native": boolean(), "wordcraft_sdk_prefix": map[string]any{"type": "string", "pattern": "^/mooring-sdk/generated/[0-9a-f]{64}$"}}, "enabled", "wordcraft_native", "gridcraft_native"),
		"DocumentInitializationState": obj(map[string]any{"schema": integer(), "rev": integer(), "epoch": integer(), "generation": integer(), "incarnation": str(), "snapshot": str(), "snapshotRev": integer(), "updates": arr(str()), "blocks": arr(block)}, "schema", "rev", "epoch", "generation", "incarnation", "snapshot", "snapshotRev", "updates", "blocks"),
		"DocumentInitializeReceipt":   obj(map[string]any{"id": str(), "rev": integer(), "epoch": integer(), "actor": str(), "kind": str(), "found": boolean(), "duplicate": boolean()}, "id", "rev", "epoch", "actor", "kind", "found", "duplicate"),
		"DocumentNode":                node, "DocumentState": state, "DocumentReceipt": receipt,
		"DocumentComment": comment,
		"DocumentPeople": obj(map[string]any{"people": map[string]any{"type": "array", "maxItems": 100, "items": obj(map[string]any{"id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$"}, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}}, "id", "name"),
			"description": "Only people who can read this Document, released only to a caller who can read it. Any other id is simply absent (shown as Unknown member), so the answer never confirms that an id exists."}}, "people"),
		"DocumentCommentList": obj(map[string]any{"page_id": str(), "comments": map[string]any{"type": "array", "maxItems": 1000, "items": ref("DocumentComment")}, "can_comment": boolean(), "can_resolve": boolean()}, "page_id", "comments", "can_comment", "can_resolve"),
	}
}
func documentSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	body := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{"id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}, "schema": map[string]any{"type": "integer", "enum": []int{1, 2, 3, 4, 5}}, "epoch": str(), "page_id": str()}
		for k, v := range extra {
			props[k] = v
		}
		return map[string]any{"type": "object", "properties": props, "required": append([]string{"id", "schema", "epoch"}, required...), "additionalProperties": false, "allOf": []any{map[string]any{"oneOf": []any{map[string]any{"properties": map[string]any{"schema": map[string]any{"enum": []int{1, 2}}}}, map[string]any{"properties": map[string]any{"schema": map[string]any{"enum": []int{3, 4, 5}}}, "required": []string{"page_id"}}}}}}
	}
	base := "/api/v1/pages/{slug}/document"
	out := map[string]DomainSchema{}
	get := func(path, name string) {
		out["GET "+path] = DomainSchema{Response: ref(name), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "429", "500", "502", "503"}}
	}
	get(base, "DocumentState")
	get(base+"/checkpoints", "DocumentCheckpointList")
	get(base+"/checkpoints/{checkpoint}", "DocumentCheckpoint")
	out["GET "+base+"/export"] = DomainSchema{NoRequest: true,
		Response:        map[string]any{"type": "string", "description": "UTF-8 Markdown of the confirmed document, produced by the docs engine from the same state GET /document reads. Delivered as an attachment named after the page slug."},
		ResponseMedia:   []string{"text/markdown"},
		SuccessStatuses: []string{"200"},
		ErrorStatuses:   []string{"400", "401", "403", "404", "409", "422", "429", "500", "502", "503"},
		Parameters: []map[string]any{
			{"name": "format", "in": "query", "required": false, "schema": map[string]any{"type": "string", "enum": []string{"markdown", "md"}}, "description": "Export format. Only Markdown exists; omitted means markdown."},
		},
	}
	out["POST "+base+"/restore"] = DomainSchema{
		Request: map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"page_id", "id", "schema", "epoch", "ifRev", "checkpoint"},
			"properties": map[string]any{
				"page_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"}, "id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"}, "epoch": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "checkpoint": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"},
				"schema": map[string]any{"type": "integer", "enum": []int{2, 3, 4, 5}},
				"ifRev":  map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740990},
			}},
		RequestRequired: true, Response: ref("DocumentRestoreReceipt"),
		SuccessStatuses: []string{"200"},
		ErrorStatuses:   []string{"400", "401", "403", "404", "409", "413", "422", "429", "500", "502", "503"},
		SuccessHeaders:  map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}},
	}
	for _, path := range []string{base + "/checkpoints", base + "/checkpoints/{checkpoint}"} {
		contract := out["GET "+path]
		contract.SuccessHeaders = map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
		out["GET "+path] = contract
	}
	out["GET "+base+"/people"] = DomainSchema{NoRequest: true, Response: ref("DocumentPeople"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500", "503"},
		SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}},
		Parameters: []map[string]any{
			{"name": "ids", "in": "query", "required": false, "schema": map[string]any{"type": "string"}, "description": "Comma-separated mention user ids (at most 100) to name for this reader."},
			{"name": "q", "in": "query", "required": false, "schema": map[string]any{"type": "string", "maxLength": 100}, "description": "Name search for the @-mention picker; at most 20 people who can read this Document. Not combinable with ids."},
		}}
	out["GET /api/v1/chats/{chatId}/document-results"] = DomainSchema{Response: ref("DocumentChatResults"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"401", "403", "404", "500", "503"}}
	out["GET /api/v1/documents/status"] = DomainSchema{Response: ref("DocumentAvailability"), SuccessStatuses: []string{"200"}}
	post := func(path string, request map[string]any) {
		out["POST "+base+path] = DomainSchema{Request: request, RequestRequired: true, Response: ref("DocumentReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "429", "500", "502", "503"}}
	}
	initialize := body(map[string]any{"body": ref("DocumentNode")}, "body")
	initialize["required"] = []string{"id", "schema", "body"}
	delete(initialize["properties"].(map[string]any), "epoch")
	initialize["properties"].(map[string]any)["schema"] = map[string]any{"type": "integer", "enum": []int{2, 3, 4, 5}}
	post("/initialize", initialize)
	initSchema := out["POST "+base+"/initialize"]
	initSchema.Response = map[string]any{"oneOf": []any{ref("DocumentInitializationState"), ref("DocumentInitializeReceipt")}}
	out["POST "+base+"/initialize"] = initSchema
	human := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"human"}}, "update": map[string]any{"type": "string", "maxLength": 1400000, "description": "Base64 incremental Yjs update; agent raw updates forbidden."}}, "kind", "update")
	semantic := body(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"agent"}}, "target": str(), "text": str(), "ifRev": integer(), "ifHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "target", "text", "ifRev", "ifHash")
	post("/patch", map[string]any{"oneOf": []any{human, semantic}})
	get(base+"/comments", "DocumentCommentList")
	commentText := map[string]any{"type": "string", "minLength": 1, "maxLength": 10000}
	thread := body(map[string]any{"ifRev": integer(), "block": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,80}$"}, "from": integer(), "to": integer(), "text": commentText}, "ifRev", "block", "from", "to", "text")
	reply := body(map[string]any{"ifRev": integer(), "parent": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}, "text": commentText}, "ifRev", "parent", "text")
	post("/comments", map[string]any{"oneOf": []any{thread, reply}})
	post("/comments/resolve", body(map[string]any{"ifRev": integer(), "comment": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"}, "version": map[string]any{"type": "integer", "minimum": 1}, "resolved": map[string]any{"type": "boolean"}}, "ifRev", "comment", "version", "resolved"))
	get(base+"/proposals", "DocumentProposalList")
	get(base+"/proposals/{proposal}", "DocumentProposalDetail")
	decision := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"request_id", "expected_rev"},
		"properties": map[string]any{"request_id": map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$", "description": "Idempotency key; an Idempotency-Key header must equal it."}, "expected_rev": map[string]any{"type": "integer", "minimum": 0, "maximum": 9007199254740990, "description": "The document revision the reviewer saw."}}}
	for _, verb := range []string{"accept", "reject"} {
		out["POST "+base+"/proposals/{proposal}/"+verb] = DomainSchema{Request: decision, RequestRequired: true, Response: ref("DocumentProposalReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "429", "500", "502", "503"}}
	}
	for key, schema := range out {
		if len(key) > 5 && key[:5] == "POST " {
			schema.ErrorMedia = []string{"application/json"}
			schema.ErrorResponse = map[string]any{"oneOf": []any{errorSchema(), problemSchema()}}
			out[key] = schema
		}
	}
	return out
}
