package main

// Native Craft is a separate opt-in whole-document profile. Its admission
// header is private; generation and public request identifiers are not bearers.
func craftNativeSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(properties map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	generation := func(nullable bool) map[string]any {
		return map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$", "maxLength": 19, "nullable": nullable, "description": "Canonical decimal int64 in1..9223372036854775807. Never convert to a JavaScript number."}
	}
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	holder := obj(map[string]any{"agent_id": str(), "run_id": str(), "actor_id": str(), "display_name": str(), "kind": map[string]any{"type": "string", "enum": []string{"human", "agent"}}}, "actor_id", "display_name", "kind")
	nullableHolder := map[string]any{"allOf": []any{ref("NativeCraftHolder")}, "nullable": true}
	request := obj(map[string]any{"request_id": str(), "actor_id": str(), "display_name": str(), "kind": map[string]any{"type": "string", "enum": []string{"human"}}}, "request_id", "actor_id", "display_name", "kind")
	empty := obj(map[string]any{})
	empty["additionalProperties"] = false
	generationRequest := obj(map[string]any{"generation": generation(false)}, "generation")
	generationRequest["additionalProperties"] = false
	handoffRequest := obj(map[string]any{"generation": generation(false), "request_id": str()}, "generation", "request_id")
	handoffRequest["additionalProperties"] = false
	schemas := map[string]any{
		"NativeCraftHolder":            holder,
		"NativeCraftRequest":           request,
		"NativeCraftEmptyRequest":      empty,
		"NativeCraftGenerationRequest": generationRequest,
		"NativeCraftHandoffRequest":    handoffRequest,
		"NativeCraftAvailability":      obj(map[string]any{"enabled": boolean(), "agents_enabled": boolean()}, "enabled", "agents_enabled"),
		"NativeCraftSession":           obj(map[string]any{"session_token": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "Private mounted host admission bearer; never deliver to an iframe or local draft."}, "session_id": str(), "application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft", "deckcraft"}}, "page_id": str()}, "session_token", "session_id", "application_id", "page_id"),
		"NativeCraftRenewal":           obj(map[string]any{"renewed": boolean()}, "renewed"),
		"NativeCraftHandoffReceipt":    obj(map[string]any{"request_id": str(), "generation": generation(false)}, "request_id", "generation"),
		"NativeCraftLease":             obj(map[string]any{"can_stop_agent": boolean(), "holder": nullableHolder, "mine": boolean(), "generation": generation(true), "expires_at": map[string]any{"type": "string", "format": "date-time", "nullable": true}, "can_write": boolean(), "acquired_at": map[string]any{"type": "string", "nullable": true, "description": "Currently always null: acquisition wall time is not persisted."}, "handoff_requested_kind": map[string]any{"type": "string", "nullable": true, "enum": []any{"human", nil}}, "handoff_requested_by": map[string]any{"type": "array", "items": ref("NativeCraftRequest")}}, "holder", "mine", "generation", "expires_at", "can_stop_agent", "can_write", "acquired_at", "handoff_requested_kind", "handoff_requested_by"),
	}
	prefix := "/api/v1/pages/{slug}/craft"
	routes := map[string]DomainSchema{}
	add := func(method, path, req, res, status string, admission bool) {
		d := DomainSchema{Response: ref(res), SuccessStatuses: []string{status}}
		if req != "" {
			d.Request = ref(req)
			d.RequestRequired = true
		} else {
			d.NoRequest = true
		}
		if admission {
			d.Parameters = []map[string]any{{"name": "X-Craft-Session", "in": "header", "required": true, "description": "Private admission issued for this mounted original human credential and Page.", "schema": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}}
		}
		routes[method+" "+prefix+path] = d
	}
	add("GET", "/availability", "", "NativeCraftAvailability", "200", false)
	add("POST", "/sessions", "NativeCraftEmptyRequest", "NativeCraftSession", "201", false)
	add("POST", "/sessions/reauthorize", "", "NativeCraftRenewal", "200", true)
	add("GET", "/lease", "", "NativeCraftLease", "200", true)
	for _, suffix := range []string{"/lease", "/lease/acquire"} {
		add("POST", suffix, "NativeCraftEmptyRequest", "NativeCraftLease", "200", true)
	}
	for _, suffix := range []string{"/lease/renew", "/lease/release"} {
		add("POST", suffix, "NativeCraftGenerationRequest", "NativeCraftLease", "200", true)
	}
	add("POST", "/lease/request-handoff", "NativeCraftEmptyRequest", "NativeCraftHandoffReceipt", "201", true)
	add("POST", "/lease/handoff", "NativeCraftHandoffRequest", "NativeCraftLease", "200", true)
	add("GET", "/events", "", "NativeCraftLease", "200", true)
	d := routes["GET "+prefix+"/events"]
	d.Response = map[string]any{"type": "string", "description": "SSE event: lease with NativeCraftLease JSON, or session_retired with delivery=stopped, reason=authority_changed and draft=retain|purge. Generic auth failures do not imply draft purge."}
	d.ResponseMedia = []string{"text/event-stream"}
	routes["GET "+prefix+"/events"] = d
	agentRoutes, agentSchemas := craftNativeAgentSchemaCatalog()
	for key, value := range agentRoutes {
		routes[key] = value
	}
	for key, value := range agentSchemas {
		schemas[key] = value
	}
	documentRoutes, documentSchemas := craftNativeDocumentSchemaCatalog()
	for key, value := range documentRoutes {
		routes[key] = value
	}
	for key, value := range documentSchemas {
		schemas[key] = value
	}
	return routes, schemas
}
