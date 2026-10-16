package main

func slideSelectedComponents() map[string]any {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	number := func(min int) map[string]any {
		return map[string]any{"type": "integer", "minimum": min, "format": "int64"}
	}
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	object := func(p map[string]any, r ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p, "required": r}
	}
	selection := object(map[string]any{"selection_schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "kind": map[string]any{"type": "string", "enum": []string{"text_range"}}, "slide_id": id(), "element_id": id(), "element_hash": str(), "from": number(0), "to": number(1), "quote": str()}, "selection_schema_version", "kind", "slide_id", "element_id", "element_hash", "from", "to", "quote")
	selection["description"] = "Immutable nonempty UTF16 range, exact quote and original element hash; surrogate boundaries cannot split. No images or other element types."
	summary := object(map[string]any{"id": id(), "page_id": id(), "schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "version": number(1), "status": map[string]any{"type": "string", "enum": []string{"queued", "running", "succeeded", "failed", "cancelled", "interrupted"}}, "base_revision": number(1), "base_schema_version": map[string]any{"type": "integer", "enum": []int{1, 2}}, "base_content_hash": str(), "selection": ref("SlideSelectedTextRange"), "instruction": str(), "human_actor": str(), "agent_actor": str(), "original_run_id": str(), "result_revision": number(1), "result_hash": str(), "error": str(), "restored": boolean(), "created_at": str(), "updated_at": str()}, "id", "page_id", "schema_version", "version", "status", "base_revision", "base_schema_version", "base_content_hash", "selection", "instruction", "human_actor", "agent_actor", "original_run_id", "error", "restored", "created_at", "updated_at")
	event := object(map[string]any{"version": number(1), "kind": map[string]any{"type": "string", "enum": []string{"queued", "dispatched", "bound", "result", "succeeded", "failed", "cancelled", "interrupted"}}, "actor": str(), "detail": str(), "created_at": str(), "mutation_id": id()}, "version", "kind", "actor", "detail", "created_at")
	history := map[string]any{"type": "array", "maxItems": 8, "items": ref("SlideSelectedEvent")}
	admission := object(map[string]any{"id": id(), "request_id": id(), "version": number(1), "status": str(), "duplicate": boolean()}, "id", "request_id", "version", "status", "duplicate")
	cancel := object(map[string]any{"mutation_id": id(), "request_id": id(), "version": number(1), "status": str(), "duplicate": boolean()}, "mutation_id", "request_id", "version", "status", "duplicate")
	return map[string]any{"SlideSelectedTextRange": selection, "SlideSelectedSummary": summary, "SlideSelectedEvent": event, "SlideSelectedRequests": object(map[string]any{"items": map[string]any{"type": "array", "maxItems": 100, "items": ref("SlideSelectedSummary")}, "next_cursor": str(), "watermark": number(0), "has_more": boolean(), "can_dispatch": boolean(), "can_cancel": boolean(), "eligible_chats": map[string]any{"type": "array", "maxItems": 50, "items": object(map[string]any{"chat_id": id(), "title": str(), "agent_id": id(), "agent_name": str(), "agent_slug": str()}, "chat_id", "title", "agent_id", "agent_name", "agent_slug")}}, "items", "next_cursor", "watermark", "has_more", "can_dispatch", "can_cancel", "eligible_chats"), "SlideSelectedRequest": object(map[string]any{"request": ref("SlideSelectedSummary"), "history": history}, "request", "history"), "SlideSelectedAdmission": admission, "SlideSelectedCancel": cancel}

}

func slideSelectedCatalog() map[string]DomainSchema {
	id := func() map[string]any { return map[string]any{"type": "string", "pattern": "^[a-zA-Z0-9_-]{1,128}$"} }
	str := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	one := func() map[string]any { return map[string]any{"type": "integer", "enum": []int{1}} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1, "format": "int64"} }
	object := func(p map[string]any, r ...string) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "properties": p, "required": r, "description": "Strict case-sensitive keys, no duplicates, nulls or trailing JSON. Immutable page_id and original preconditions. Live original credential and permissions required, including exact replay."}
	}
	base := "/api/v1/pages/{slug}/slides/requests"
	out := map[string]DomainSchema{}
	read := func(path, response string) {
		out["GET "+path] = DomainSchema{Response: ref(response), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "500"}}
	}
	post := func(path, response, status string, body map[string]any) {
		out["POST "+path] = DomainSchema{RequestRequired: true, Request: body, Response: ref(response), SuccessStatuses: []string{status}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500", "503"}}
	}
	read(base, "SlideSelectedRequests")
	paged := out["GET "+base]
	paged.Parameters = []map[string]any{{"name": "cursor", "in": "query", "schema": str(1024)}, {"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25}}}
	out["GET "+base] = paged
	read(base+"/{requestId}", "SlideSelectedRequest")
	post(base, "SlideSelectedAdmission", "202", object(map[string]any{"page_id": id(), "type_id": map[string]any{"type": "string", "enum": []string{"slides"}}, "schema_version": one(), "base_rev": integer(), "submission_id": id(), "chat_id": id(), "instruction": str(4096), "selection": ref("SlideSelectedTextRange")}, "page_id", "type_id", "schema_version", "base_rev", "submission_id", "chat_id", "instruction", "selection"))
	post(base+"/{requestId}/cancel", "SlideSelectedCancel", "200", object(map[string]any{"page_id": id(), "schema_version": one(), "mutation_id": id(), "if_version": integer()}, "page_id", "schema_version", "mutation_id", "if_version"))
	return out
}
