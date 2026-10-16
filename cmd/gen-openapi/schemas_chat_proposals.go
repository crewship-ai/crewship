package main

// Functional proposal routes reuse the frozen A1 field names and bounds.
func chatProposalSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func(min, max int) map[string]any {
		return map[string]any{"type": "string", "minLength": min, "maxLength": max}
	}
	obj := func(p map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false}
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	integer := map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}
	nullableID := map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "nullable": true}
	item := obj(map[string]any{"id": str(1, 200), "title": str(1, 240), "description": str(0, 10000), "crew_id": str(1, 200), "project_id": nullableID, "assignee_id": nullableID, "priority": map[string]any{"type": "string", "enum": []string{"none", "urgent", "high", "medium", "low"}}, "selected": map[string]any{"type": "boolean"}}, "id", "title", "description", "crew_id", "project_id", "assignee_id", "priority", "selected")
	items := map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": ref("ChatProposalItem")}
	outcome := obj(map[string]any{"reviewed_version": integer, "confirmed_by": str(1, 200), "confirmed_at": map[string]any{"type": "string", "format": "date-time"}, "issues": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": obj(map[string]any{"id": str(1, 200), "identifier": str(1, 200)}, "id", "identifier")}}, "reviewed_version", "confirmed_by", "confirmed_at", "issues")
	outcome["nullable"] = true
	proposal := obj(map[string]any{"schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "version": integer, "status": map[string]any{"type": "string", "enum": []string{"draft", "accepted", "discarded", "expired"}}, "title": str(1, 240), "items": items, "expires_at": map[string]any{"type": "string", "format": "date-time"}, "allowed_actions": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"view", "edit", "confirm", "discard"}}}, "outcome": outcome}, "schema_version", "version", "status", "title", "items", "expires_at", "allowed_actions", "outcome")
	// The A1 envelope shares E1's anchor/provenance but has no Issue current state.
	_, existing := chatEntitySchemaCatalog()
	base := existing["ChatEntityReference"].(map[string]any)
	props := map[string]any{}
	for k, v := range base["properties"].(map[string]any) {
		if k != "current" {
			props[k] = v
		}
	}
	props["proposal"] = ref("ChatProposal")
	props["kind"] = map[string]any{"type": "string", "enum": []string{"proposal"}}
	card := obj(props, "event_id", "operation_id", "kind", "verb", "entity", "provenance", "anchor", "revision_at_action", "proposal")
	page := obj(map[string]any{"contract_version": map[string]any{"type": "integer", "enum": []int{1}}, "reset": map[string]any{"type": "boolean"}, "next_cursor": str(0, 4096), "snapshot_id": str(1, 200), "snapshot_complete": map[string]any{"type": "boolean"}, "has_more": map[string]any{"type": "boolean"}, "refs": map[string]any{"type": "array", "items": ref("ChatProposalResponse")}}, "contract_version", "reset", "next_cursor", "snapshot_id", "snapshot_complete", "has_more", "refs")
	edit := obj(map[string]any{"expected_version": integer, "title": str(1, 240), "items": items}, "expected_version", "title", "items")
	confirm := obj(map[string]any{"proposal_id": str(1, 200), "reviewed_version": integer}, "proposal_id", "reviewed_version")
	discard := obj(map[string]any{"expected_version": integer}, "expected_version")
	components := map[string]any{"ChatProposalItem": item, "ChatProposal": proposal, "ChatProposalResponse": card, "ChatProposalPage": page, "ChatProposalEdit": edit, "ChatProposalConfirm": confirm, "ChatProposalDiscard": discard}
	path := "/api/v1/chats/{chatId}/proposals"
	routes := map[string]DomainSchema{}
	for key, name := range map[string]string{"GET " + path: "ChatProposalPage", "GET " + path + "/{sourceId}": "ChatProposalResponse", "PATCH " + path + "/{sourceId}": "ChatProposalEdit", "POST " + path + "/{sourceId}/confirm": "ChatProposalConfirm", "POST " + path + "/{sourceId}/discard": "ChatProposalDiscard"} {
		schema := DomainSchema{Response: ref("ChatProposalResponse"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "403", "404", "409", "500"}, SuccessHeaders: map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}}
		if key[:3] == "GET" {
			schema.Response = ref(name)
		} else {
			schema.Request = ref(name)
			schema.RequestRequired = true
			schema.Parameters = []map[string]any{{"name": "Idempotency-Key", "in": "header", "required": true, "schema": str(1, 200), "description": "Stable actor-scoped operation key; exact retry returns original authorized result. Different intent returns 409."}}
			schema.ErrorResponse = map[string]any{"anyOf": []any{errorSchema(), obj(map[string]any{"error": str(1, 200), "current": ref("ChatProposalResponse")}, "error", "current")}}
		}
		routes[key] = schema
	}
	list := routes["GET "+path]
	list.Parameters = []map[string]any{{"name": "cursor", "in": "query", "schema": str(0, 4096)}}
	routes["GET "+path] = list
	return routes, components
}
