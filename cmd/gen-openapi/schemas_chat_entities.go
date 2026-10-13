package main

func chatEntitySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 0} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(properties map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required}
	}
	actor := obj(map[string]any{"type": str(), "id": str()}, "type", "id")
	entity := obj(map[string]any{"id": str(), "identifier": str(), "workspace_id": str(), "deleted": boolean()}, "id", "identifier", "workspace_id")
	provenance := obj(map[string]any{"actor": actor, "at": map[string]any{"type": "string", "format": "date-time"}}, "actor", "at")
	revision := obj(map[string]any{"revision": integer(), "aggregate_revision": integer()}, "revision", "aggregate_revision")
	anchor := obj(map[string]any{"trigger_message_id": str(), "run_id": str(), "run_status": map[string]any{"type": "string", "nullable": true, "enum": []any{"running", "completed", "failed", "cancelled", "timeout", nil}}}, "trigger_message_id", "run_id", "run_status")
	current := obj(map[string]any{"title": str(), "status": str(), "priority": str(), "assignee_type": map[string]any{"type": "string", "nullable": true}, "assignee_id": map[string]any{"type": "string", "nullable": true}, "revision": integer(), "aggregate_revision": integer(), "allowed_actions": map[string]any{"type": "array", "items": str()}}, "title", "status", "priority", "assignee_type", "assignee_id", "revision", "aggregate_revision", "allowed_actions")
	item := obj(map[string]any{"event_id": str(), "operation_id": str(), "current": current, "kind": str(), "verb": str(), "entity": ref("ChatEntityIdentity"), "provenance": ref("ChatEntityProvenance"), "anchor": anchor, "revision_at_action": ref("IssueEntityRevision")}, "event_id", "operation_id", "kind", "verb", "entity", "provenance", "anchor", "revision_at_action")
	page := obj(map[string]any{"reset": boolean(), "next_cursor": str(), "snapshot_id": str(), "snapshot_complete": boolean(), "has_more": boolean(), "refs": map[string]any{"type": "array", "items": ref("ChatEntityReference")}}, "reset", "next_cursor", "snapshot_id", "snapshot_complete", "has_more", "refs")
	operation := obj(map[string]any{"id": str(), "status": str(), "kind": str(), "verb": str(), "entity": ref("ChatEntityIdentity"), "provenance": ref("ChatEntityProvenance"), "revision_at_action": ref("IssueEntityRevision")}, "id", "status", "kind", "verb", "entity", "provenance", "revision_at_action")
	components := map[string]any{"ChatEntityIdentity": entity, "ChatEntityProvenance": provenance, "IssueEntityRevision": revision, "ChatEntityReference": item, "ChatEntityReferencePage": page, "ChatEntityOperation": operation}
	path := func(name string) map[string]any {
		return map[string]any{"name": name, "in": "path", "required": true, "schema": str()}
	}
	noStore := map[string]any{"Cache-Control": map[string]any{"description": "Authorization-sensitive projection; never store in shared caches.", "schema": str()}}
	routes := map[string]DomainSchema{}
	for route, schema := range map[string]string{
		"GET /api/v1/chats/{chatId}/entity-refs":                            "ChatEntityReferencePage",
		"GET /api/v1/operations/{operationId}":                              "ChatEntityOperation",
		"GET /api/v1/issues/{identifier}/revision":                          "IssueEntityRevision",
		"GET /api/v1/workspaces/{workspaceId}/issues/{identifier}/revision": "IssueEntityRevision",
	} {
		routes[route] = DomainSchema{Response: ref(schema), SuccessStatuses: []string{"200"}, SuccessHeaders: noStore}
	}
	route := routes["GET /api/v1/chats/{chatId}/entity-refs"]
	route.Parameters = []map[string]any{path("chatId"), {"name": "cursor", "in": "query", "schema": str()}, {"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100}}}
	routes["GET /api/v1/chats/{chatId}/entity-refs"] = route
	for _, key := range []string{"PATCH /api/v1/crews/{crewId}/issues/{identifier}", "POST /api/v1/crews/{crewId}/issues/{identifier}/comments"} {
		params := []map[string]any{{"name": "Idempotency-Key", "in": "header", "schema": map[string]any{"type": "string", "maxLength": 200}, "description": "Stable UTF-8 key of at most 200 bytes, scoped to the authenticated user. Same key with changed intent returns 409."}}
		if key[:5] == "PATCH" {
			params = append(params, map[string]any{"name": "If-Match", "in": "header", "schema": str(), "description": "Expected positive revision, optionally quoted; must agree with expected_revision when both are provided."})
		}
		routes[key] = DomainSchema{Parameters: params}
	}
	return routes, components
}
