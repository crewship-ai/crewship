package main

func slideCommentComponents() map[string]any {
	stringValue := func() map[string]any { return map[string]any{"type": "string"} }
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "format": "int64"}
	}
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	object := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	anchor := object(map[string]any{"anchor_schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "kind": enum("slide", "element"), "slide_id": id(), "element_id": id(), "revision": integer(1)}, "anchor_schema_version", "kind", "slide_id", "revision")
	anchor["description"] = "Immutable original identity at a pinned revision. element_id required exactly for kind element; forbidden for kind slide. Anchor schema evolves separately from deck schema."
	event := object(map[string]any{"version": integer(1), "kind": enum("comment", "reply", "resolve", "reopen"), "body": stringValue(), "actor": stringValue(), "mutation_id": id(), "created_at": stringValue()}, "version", "kind", "body", "actor", "mutation_id", "created_at")
	thread := object(map[string]any{"id": id(), "page_id": id(), "anchor": ref("SlideCommentAnchor"), "version": integer(1), "resolved": boolean(), "status": enum("attached", "detached", "restored"), "projection_revision": integer(1), "last_missing_revision": integer(1), "restored_at_revision": integer(1), "created_at": stringValue(), "updated_at": stringValue(), "comment_count": integer(1), "first_comment": ref("SlideCommentEvent")}, "id", "page_id", "anchor", "version", "resolved", "status", "projection_revision", "created_at", "updated_at", "comment_count", "first_comment")
	receipt := object(map[string]any{"mutation_id": id(), "thread_id": id(), "version": integer(1), "duplicate": boolean()}, "mutation_id", "thread_id", "version", "duplicate")
	page := func(item string) map[string]any {
		return object(map[string]any{"items": map[string]any{"type": "array", "items": ref(item), "maxItems": 100}, "next_cursor": stringValue(), "watermark": integer(0), "has_more": boolean(), "can_comment": boolean(), "can_resolve": boolean()}, "items", "next_cursor", "watermark", "has_more", "can_comment", "can_resolve")
	}
	single := object(map[string]any{"thread": ref("SlideCommentThread"), "can_comment": boolean(), "can_resolve": boolean()}, "thread", "can_comment", "can_resolve")
	return map[string]any{"SlideCommentAnchor": anchor, "SlideCommentEvent": event, "SlideCommentThread": thread, "SlideCommentReceipt": receipt, "SlideCommentThreads": page("SlideCommentThread"), "SlideCommentHistory": page("SlideCommentEvent"), "SlideCommentThreadResponse": single}
}

func slideCommentCatalog() map[string]DomainSchema {
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1, "format": "int64"} }
	body := func() map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": 10000, "description": "Nonblank UTF-8, at most10000 bytes; LF/tab allowed, CR and other controls rejected."}
	}
	request := func(properties map[string]any, required ...string) map[string]any {
		properties["page_id"] = id()
		properties["schema_version"] = map[string]any{"type": "integer", "enum": []int{1}}
		properties["mutation_id"] = id()
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": append([]string{"page_id", "schema_version", "mutation_id"}, required...), "description": "Human-only, exact case-sensitive keys with no duplicates or trailing JSON; at most16KiB. Immutable page_id, stable mutation_id and explicit preconditions; live credential and ACL rechecked even on receipt replay."}
	}
	out := map[string]DomainSchema{}
	base := "/api/v1/pages/{slug}/slides/comments"
	get := func(path, response string, paginated bool) {
		schema := DomainSchema{Response: ref(response), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500"}}
		if paginated {
			schema.Parameters = []map[string]any{
				{"name": "limit", "in": "query", "required": false, "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25}},
				{"name": "cursor", "in": "query", "required": false, "schema": map[string]any{"type": "string", "maxLength": 1024, "description": "Opaque Page/thread-bound continuation with membership watermark. Live ACL required on every page."}},
			}
		}
		out["GET "+base+path] = schema
	}
	get("", "SlideCommentThreads", true)
	get("/{threadId}", "SlideCommentThreadResponse", false)
	get("/{threadId}/history", "SlideCommentHistory", true)
	post := func(path string, body map[string]any) {
		out["POST "+base+path] = DomainSchema{RequestRequired: true, Request: body, Response: ref("SlideCommentReceipt"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500"}}
	}
	create := request(map[string]any{"base_rev": integer(), "anchor": ref("SlideCommentAnchor"), "body": body()}, "base_rev", "anchor", "body")
	create["description"] = "COMMENT permission; anchor.revision must equal original base_rev and current deck revision. Anchor must exist at admission. Thread ID equals create mutation ID in this Page namespace."
	post("", create)
	post("/{threadId}/replies", request(map[string]any{"if_version": integer(), "body": body()}, "if_version", "body"))
	post("/{threadId}/resolve", request(map[string]any{"if_version": integer(), "resolved": map[string]any{"type": "boolean"}}, "if_version", "resolved"))
	return out
}
