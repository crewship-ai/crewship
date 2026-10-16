package main

func requestExecutionProperties() map[string]any {
	return map[string]any{
		"retry_of_request_id": map[string]any{"type": "string", "maxLength": 128},
		"retry_warning":       map[string]any{"type": "string"},
		"execution_state":     map[string]any{"type": "string", "enum": []string{"observed", "awaiting_confirmation", "unknown", "deadline_exceeded"}},
		"heartbeat_at":        map[string]any{"type": "string", "format": "date-time", "nullable": true, "description": "Actual work attempt heartbeat only; null when no such evidence exists."},
		"last_activity_at":    map[string]any{"type": "string", "format": "date-time", "nullable": true},
		"deadline_at":         map[string]any{"type": "string", "format": "date-time", "nullable": true},
		"deadline_source":     map[string]any{"type": "string", "enum": []string{"recorded", "legacy_derived"}},
		"run_status":          map[string]any{"type": "string", "enum": []string{"COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"}, "description": "Confirmed terminal run outcome. Omitted when unconfirmed; independent of the request result."},
	}
}

func boardRequestSchemaCatalog() map[string]DomainSchema {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer", "minimum": 0}
	number := map[string]any{"type": "number"}
	boolean := map[string]any{"type": "boolean"}
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	geometry := obj(map[string]any{"x": number, "y": number, "width": number, "height": number}, "x", "y", "width", "height")
	props := map[string]any{"id": str, "kind": map[string]any{"type": "string", "enum": []string{"diagram", "artifact"}}, "status": map[string]any{"type": "string", "enum": []string{"queued", "running", "ready_to_insert", "inserted", "succeeded", "failed", "cancelled"}}, "base_seq": integer, "instruction": str, "selected_element_ids": array(str), "selected_placement_ids": array(str), "target": geometry, "agent_id": str, "agent_name": str, "run_id": str, "human_user_id": str, "created_at": str, "updated_at": str, "error": str, "dialect": str, "converter_version": str, "converter_url": str, "source_hash": str, "can_insert": boolean, "can_cancel": boolean, "result_seq": map[string]any{"type": "integer", "nullable": true}, "inserted_by_user_id": str, "result": obj(map[string]any{"page_slug": str, "title": str, "revision": integer}, "page_slug", "title", "revision"), "pin_status": str, "pin_reason": str, "result_placement_id": str, "has_preview": boolean}
	props = mergeProps(props, requestExecutionProperties())
	record := obj(props, "id", "kind", "status", "base_seq", "instruction", "selected_element_ids", "selected_placement_ids", "target", "created_at", "updated_at", "can_insert", "can_cancel", "result_seq", "converter_url")
	detail := obj(mergeProps(props, map[string]any{"mermaid_source": str, "inserted_element_ids": array(str), "edited_since_insert": boolean}), "id", "kind", "status", "mermaid_source", "source_hash", "converter_version", "converter_url", "inserted_element_ids", "edited_since_insert")
	target := obj(map[string]any{"chat_id": str, "title": str, "agent_id": str, "agent_name": str, "agent_slug": str}, "chat_id", "agent_id", "agent_name", "agent_slug")
	create := obj(map[string]any{"kind": props["kind"], "chat_id": str, "instruction": str, "base_seq": integer, "submission_id": str, "retry_of_request_id": map[string]any{"type": "string", "maxLength": 128}, "selected_element_ids": array(str), "selected_placement_ids": array(str), "target": geometry, "dialect": str, "converter_version": str, "preview_png": map[string]any{"type": "string", "format": "byte", "description": "Optional untrusted PNG illustration, <=1MiB decoded, <=1024x1024. Canonical snapshot alone provides authority."}}, "kind", "chat_id", "instruction", "base_seq", "submission_id", "target")
	insert := obj(map[string]any{"mutation_id": str, "base_seq": integer, "source_hash": str, "converter_version": str, "upserts": array(obj(map[string]any{"element": map[string]any{"type": "object", "additionalProperties": true}, "expected_rev": map[string]any{"type": "integer", "enum": []int{0}}}, "element", "expected_rev"))}, "mutation_id", "base_seq", "source_hash", "converter_version", "upserts")
	receipt := obj(map[string]any{"id": str, "status": str, "result_seq": integer, "already_inserted": boolean, "inserted_element_ids": array(str), "element_revs": map[string]any{"type": "object", "additionalProperties": integer}, "undo_expires_at": str}, "id", "status", "result_seq", "already_inserted")
	return map[string]DomainSchema{
		"GET /api/v1/page-request-results/{kind}/{requestId}":         {Response: obj(map[string]any{"page_slug": str, "revision": map[string]any{"type": "integer", "minimum": 1}}, "page_slug")},
		"GET /api/v1/pages/{slug}/board/requests":                     {Response: obj(map[string]any{"can_dispatch": boolean, "eligible_chats": array(target), "converter_url": str, "requests": array(record)}, "can_dispatch", "eligible_chats", "converter_url", "requests")},
		"POST /api/v1/pages/{slug}/board/requests":                    {Request: create, RequestRequired: true, Response: obj(map[string]any{"id": str, "retry_of_request_id": str, "retry_warning": str}, "id"), SuccessStatuses: []string{"202"}},
		"GET /api/v1/pages/{slug}/board/requests/{requestId}":         {Response: detail},
		"POST /api/v1/pages/{slug}/board/requests/{requestId}/cancel": {Request: obj(map[string]any{}), Response: detail},
		"POST /api/v1/pages/{slug}/board/requests/{requestId}/insert": {Request: insert, RequestRequired: true, Response: receipt, SuccessStatuses: []string{"200", "201"}},
		"GET /api/v1/pages/runtime/board-converter":                   {Response: map[string]any{"type": "string"}, ResponseMedia: []string{"text/html"}},
	}
}

func pageRequestResultSchema() DomainSchema {
	return DomainSchema{Response: map[string]any{"type": "object", "properties": map[string]any{"page_slug": map[string]any{"type": "string"}, "revision": map[string]any{"type": "integer", "minimum": 1}}, "required": []string{"page_slug"}, "additionalProperties": false}}
}
