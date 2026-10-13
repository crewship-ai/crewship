package main

func chatEntitySchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer", "minimum": 1} }
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
	item := obj(map[string]any{"event_id": str(), "kind": str(), "verb": str(), "entity": ref("ChatEntityIdentity"), "provenance": ref("ChatEntityProvenance"), "anchor": anchor, "revision_at_action": ref("IssueEntityRevision")}, "event_id", "kind", "verb", "entity", "provenance", "anchor", "revision_at_action")
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
	return routes, components
}
