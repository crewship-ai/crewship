package main

// restrictedWorkflowSchemaCatalog describes the private queue projection. The
// ordinary run route also accepts its historical trusted request contract.
func restrictedWorkflowSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }
	arr := func(s map[string]any) map[string]any { return map[string]any{"type": "array", "items": s} }
	obj := func(p map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "additionalProperties": false, "properties": p}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	state := map[string]any{"type": "string", "enum": []string{"pending", "running", "completed", "failed"}}
	components := map[string]any{
		"RestrictedWorkflowRunRequest": obj(map[string]any{"inputs": map[string]any{"type": "object", "additionalProperties": true}, "expected_definition_hash": str(), "expected_execution_hash": str(), "delay_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 86400}}),
		"RestrictedWorkflowReceipt":    obj(map[string]any{"run_id": str(), "chat_id": str(), "status": state, "restricted": map[string]any{"type": "boolean", "enum": []bool{true}}, "status_url": str()}, "run_id", "chat_id", "status", "restricted", "status_url"),
		"RestrictedWorkflowResult":     obj(map[string]any{"run_id": str(), "status": state, "step_outputs": map[string]any{"type": "object", "additionalProperties": str()}, "created_at": map[string]any{"type": "string", "format": "date-time"}}, "run_id", "status", "step_outputs", "created_at"),
		"RestrictedRoutineInput":       obj(map[string]any{"name": str(), "type": map[string]any{"type": "string", "enum": []string{"string", "number", "boolean"}}, "required": map[string]any{"type": "boolean"}}, "name", "type", "required"),
		"RestrictedRoutine":            obj(map[string]any{"slug": str(), "name": str(), "definition_hash": str(), "execution_hash": str(), "inputs": arr(ref("RestrictedRoutineInput"))}, "slug", "name", "definition_hash", "execution_hash", "inputs"),
	}

	components["RestrictedPageInput"] = obj(map[string]any{"name": str(), "label": str(), "type": map[string]any{"type": "string", "enum": []string{"text", "textarea", "number", "boolean", "select"}}, "required": map[string]any{"type": "boolean"}, "options": arr(str())}, "name", "type", "required")
	components["RestrictedPageConfirmation"] = obj(map[string]any{"title": str(), "body": str(), "confirm_label": str(), "cancel_label": str()}, "title", "body")
	components["RestrictedPageAction"] = obj(map[string]any{"intent_hash": str(), "panel_id": str(), "id": str(), "label": str(), "inputs": arr(ref("RestrictedPageInput")), "confirm": ref("RestrictedPageConfirmation")}, "intent_hash", "panel_id", "id", "label", "inputs")
	components["RestrictedPage"] = obj(map[string]any{"slug": str(), "name": str(), "publication": map[string]any{"type": "integer", "minimum": 1}, "actions": arr(ref("RestrictedPageAction"))}, "slug", "name", "actions")
	prefix := "/api/v1/workspaces/{workspaceId}/"
	routes := map[string]DomainSchema{
		"GET " + prefix + "restricted-routines":             {Response: arr(ref("RestrictedRoutine")), SuccessStatuses: []string{"200"}, SuccessHeaders: map[string]any{"X-Total-Count": map[string]any{"description": "Number of currently authorized routines", "schema": map[string]any{"type": "integer", "minimum": 0}}}},
		"GET " + prefix + "restricted-routine-runs":         {Response: arr(ref("RestrictedWorkflowResult")), SuccessStatuses: []string{"200"}},
		"GET " + prefix + "restricted-routine-runs/{runId}": {Response: ref("RestrictedWorkflowResult"), SuccessStatuses: []string{"200"}},
	}

	routes["GET "+prefix+"restricted-pages"] = DomainSchema{Response: arr(ref("RestrictedPage")), SuccessStatuses: []string{"200"}, SuccessHeaders: map[string]any{"X-Total-Count": map[string]any{"description": "Number of Pages with currently authorized executable actions", "schema": map[string]any{"type": "integer", "minimum": 0}}}}
	// Reuse the existing route's trusted response rather than narrowing its
	// behavior when the authenticated member uses the trusted execution mode.
	trustedRoutes, _ := remainingCrewAgentSchemaCatalogV1()
	trusted := trustedRoutes["POST "+prefix+"pipelines/{slug}/run"]
	responses := []any{ref("RestrictedWorkflowReceipt")}
	if trusted.Response != nil {
		responses = append(responses, trusted.Response)
	}
	routes["POST "+prefix+"pipelines/{slug}/run"] = DomainSchema{Request: map[string]any{"anyOf": []any{ref("WorkflowPipelineRunRequest"), ref("RestrictedWorkflowRunRequest")}}, Response: map[string]any{"anyOf": responses}, SuccessStatuses: []string{"202"}}

	components["RestrictedPageWorkflowReceipt"] = obj(map[string]any{"pending_id": str(), "run_id": str(), "status": state, "deduped": map[string]any{"type": "boolean"}, "coalesced": map[string]any{"type": "boolean"}, "routine": str(), "panel": str(), "action": str(), "restricted": map[string]any{"type": "boolean", "enum": []bool{true}}, "status_url": str()}, "pending_id", "run_id", "status", "deduped", "coalesced", "routine", "panel", "action", "restricted", "status_url")
	components["RestrictedPageWorkflowStatus"] = obj(map[string]any{"pending_id": str(), "run_id": str(), "pending_status": state, "run_status": state, "restricted": map[string]any{"type": "boolean", "enum": []bool{true}}, "routine_revision_pinned": map[string]any{"type": "boolean", "enum": []bool{true}}, "step_outputs": map[string]any{"type": "object", "additionalProperties": str()}}, "pending_id", "run_id", "pending_status", "run_status", "restricted", "routine_revision_pinned", "step_outputs")
	for _, path := range []string{"/api/v1/pages/{slug}/panels/{panelId}/actions/{actionId}", "/api/v1/pages/{slug}/application/actions/{panelId}/{actionId}"} {
		previous := pagesSchemaCatalog()["POST "+path]
		if previous.Response == nil {
			previous = pageProjectSchemaCatalog()["POST "+path]
		}
		variants := []any{ref("RestrictedPageWorkflowReceipt")}
		if previous.Response != nil {
			variants = append(variants, previous.Response)
		}
		components["RestrictedPageWorkflowRequest"] = obj(map[string]any{"inputs": map[string]any{"type": "object", "additionalProperties": true}, "publication": map[string]any{"type": "integer", "minimum": 1}, "expected_intent_hash": str()}, "inputs", "expected_intent_hash")
		requests := []any{ref("RestrictedPageWorkflowRequest")}
		if previous.Request != nil {
			requests = append(requests, previous.Request)
		}
		previous.Request = map[string]any{"anyOf": requests}
		previous.Response = map[string]any{"anyOf": variants}
		previous.SuccessStatuses = []string{"202"}
		routes["POST "+path] = previous
	}
	statusPath := "GET /api/v1/pages/{slug}/application/actions/{pendingId}"
	previous := pageProjectSchemaCatalog()[statusPath]
	routes[statusPath] = DomainSchema{Response: map[string]any{"anyOf": []any{previous.Response, ref("RestrictedPageWorkflowStatus")}}, SuccessStatuses: []string{"200"}}
	return routes, components
}
