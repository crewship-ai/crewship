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
	sessionRequest := obj(map[string]any{"chat_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$", "description": "The caller's private chat linked to this workbook. Omit for ordinary native admission."}})
	sessionRequest["additionalProperties"] = false
	generationRequest := obj(map[string]any{"generation": generation(false)}, "generation")
	generationRequest["additionalProperties"] = false
	handoffRequest := obj(map[string]any{"generation": generation(false), "request_id": str()}, "generation", "request_id")
	handoffRequest["additionalProperties"] = false
	schemas := map[string]any{
		"NativeCraftHolder":            holder,
		"NativeCraftRequest":           request,
		"NativeCraftEmptyRequest":      empty,
		"NativeCraftSessionRequest":    sessionRequest,
		"NativeCraftGenerationRequest": generationRequest,
		"NativeCraftHandoffRequest":    handoffRequest,
		"NativeCraftAvailability":      obj(map[string]any{"enabled": boolean(), "can_edit": boolean(), "preview_enabled": boolean(), "agents_enabled": boolean(), "applications": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "enum": []string{"open", "draining", "closed"}}}, "application": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}, "description": "This document's application, present when the caller may open it."}, "edit_refusal": map[string]any{"type": "string", "enum": []string{"app_draining", "app_disabled"}, "description": "Present when the caller has Edit but the instance administrator turned the application off or it is draining; can_edit is then false. The same reason lease acquire answers with."}}, "enabled", "can_edit", "preview_enabled", "agents_enabled"),
		"NativeCraftSession":           obj(map[string]any{"session_token": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$", "description": "Private mounted host admission bearer; never deliver to an iframe or local draft."}, "session_id": str(), "application_id": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "page_id": str(), "realtime_sheet": boolean(), "can_write": boolean(), "generation": generation(false)}, "session_token", "session_id", "application_id", "page_id", "realtime_sheet", "can_write"),
		"NativeCraftRenewal":           obj(map[string]any{"renewed": boolean()}, "renewed"),
		"NativeCraftHandoffReceipt":    obj(map[string]any{"request_id": str(), "generation": generation(false)}, "request_id", "generation"),
		"NativeCraftLease":             obj(map[string]any{"realtime_sheet": boolean(), "can_stop_agent": boolean(), "holder": nullableHolder, "mine": boolean(), "generation": generation(true), "expires_at": map[string]any{"type": "string", "format": "date-time", "nullable": true}, "can_write": boolean(), "can_share": boolean(), "can_edit": boolean(), "acquired_at": map[string]any{"type": "string", "nullable": true, "description": "Currently always null: acquisition wall time is not persisted."}, "handoff_requested_kind": map[string]any{"type": "string", "nullable": true, "enum": []any{"human", nil}}, "handoff_requested_by": map[string]any{"type": "array", "items": ref("NativeCraftRequest")}}, "holder", "mine", "generation", "expires_at", "can_stop_agent", "can_write", "can_edit", "acquired_at", "handoff_requested_kind", "handoff_requested_by", "can_share"),
	}

	schemas["NativeCraftPreview"] = obj(map[string]any{"version": generation(false), "bytes": map[string]any{"type": "string", "format": "byte"}, "applicationId": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "enginePin": str(), "editorBundle": str(), "editorURL": str(), "format": map[string]any{"type": "string", "enum": []string{"xlsx", "xlsm", "native"}}}, "version", "bytes", "applicationId", "enginePin", "editorBundle", "editorURL")
	schemas["NativeCraftShareRequest"] = obj(map[string]any{"workspaceId": str(), "pageId": str(), "applicationId": str(), "enginePin": str(), "sessionId": str(), "generation": generation(false), "baseVersion": generation(false), "targetVersion": generation(false)}, "workspaceId", "pageId", "applicationId", "enginePin", "sessionId", "generation", "baseVersion", "targetVersion")
	schemas["NativeCraftShareRequest"].(map[string]any)["additionalProperties"] = false
	schemas["NativeCraftShareReceipt"] = obj(map[string]any{"version": generation(false), "durability": map[string]any{"type": "string", "enum": []string{"node"}}, "issuedBy": map[string]any{"type": "string", "enum": []string{"crewship"}}, "phase": map[string]any{"type": "string", "enum": []string{"shared"}}}, "version", "durability", "issuedBy", "phase")
	schemas["NativeCraftAgentAccess"] = obj(map[string]any{
		"allowed":         boolean(),
		"reason":          map[string]any{"type": "string", "enum": []string{"feature_off", "agent_policy", "not_attached", "holder_human", "engine_failed", "preview_only"}, "description": "Why the agent may not change this Sheet: the reason the agent itself would be told. Absent when allowed."},
		"message":         map[string]any{"type": "string", "description": "The reason as a sentence."},
		"detail":          map[string]any{"type": "string", "description": "A finer code for the caller, who may read the Sheet: workspace_flag_off, chat_not_private, not_a_sheet, not_owner, not_in_personal_space, no_write_access, agent_unavailable, agents_app_disabled, or an application status reason. New codes may be added."},
		"holder":          obj(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"human", "agent"}}, "mine": map[string]any{"type": "boolean", "description": "The caller's own editor holds it."}}, "kind", "mine"),
		"max_sheets":      map[string]any{"type": "integer", "description": "How many Sheets one message may attach for the agent to change; with more it may change none."},
		"default_allowed": map[string]any{"type": "boolean", "description": "Start the consent switch on: allowed, and the Sheet came into this very chat (uploaded or attached here). It permits nothing; consent is still the sent message."},
		"head_version":    map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,17}$", "description": "The Sheet's latest confirmed revision, the one an agent would start from, allowed or not. Only for a caller who may read it (not Read or Comment grantees); absent before the first revision."},
		"saved_at":        map[string]any{"type": "string", "description": "When that revision was saved (ISO 8601 UTC)."},
		"saved_by_name":   map[string]any{"type": "string", "description": "Who saved it, named as in the version history."},
	}, "allowed", "max_sheets", "default_allowed")
	schemas["ChatArtifactReference"] = obj(map[string]any{
		"message_id":  str(),
		"page_id":     str(),
		"slug":        str(),
		"name":        str(),
		"application": map[string]any{"type": "string", "description": "The native Craft application of the page (gridcraft for a Sheet); absent for other pages."},
		"created_at":  str(),
		"source":      map[string]any{"type": "string", "enum": []string{"message", "upload"}, "description": "message: a message of the chat attached the page (message_id names it). upload: a Sheet opened from a file in this chat (initialize with X-Craft-Chat) that no message has attached yet; message_id is empty."},
	}, "message_id", "page_id", "slug", "name", "created_at", "source")
	schemas["ChatArtifactReferencePage"] = obj(map[string]any{
		"refs":        map[string]any{"type": "array", "items": ref("ChatArtifactReference")},
		"next_cursor": map[string]any{"type": "string", "description": "Present when more references may follow; pass it back as cursor."},
	}, "refs")
	prefix := "/api/v1/pages/{slug}/craft"
	routes := map[string]DomainSchema{}
	routes["GET /api/v1/chats/{chatId}/artifact-refs"] = DomainSchema{Response: ref("ChatArtifactReferencePage"), SuccessStatuses: []string{"200"}, ErrorStatuses: []string{"400", "401", "403", "404", "503"}, NoRequest: true, Parameters: []map[string]any{
		{"name": "chatId", "in": "path", "required": true, "schema": str()},
		{"name": "cursor", "in": "query", "schema": str()},
		{"name": "limit", "in": "query", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 100}},
	}}
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
	add("GET", "/agent-access", "", "NativeCraftAgentAccess", "200", false)
	access := routes["GET "+prefix+"/agent-access"]
	access.Parameters = []map[string]any{{"name": "chat_id", "in": "query", "required": true, "schema": map[string]any{"type": "string"}, "description": "The caller's own private chat the Sheet would be attached in."}}
	routes["GET "+prefix+"/agent-access"] = access
	add("GET", "/preview", "", "NativeCraftPreview", "200", false)
	add("POST", "/document/share", "NativeCraftShareRequest", "NativeCraftShareReceipt", "200", true)
	add("POST", "/sessions", "NativeCraftSessionRequest", "NativeCraftSession", "201", false)
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
	d.Response = map[string]any{"type": "string", "description": "SSE event: lease with NativeCraftLease JSON for ordinary sessions. Real-time chat workbook sessions receive sheet_agent_presence_snapshot (NativeCraftSheetPresenceSnapshot) and sheet_agent_presence (NativeCraftSheetPresence). Either mode can receive session_retired with delivery=stopped, reason=authority_changed|write_revoked and draft=retain|purge; write_revoked (always with draft=retain) means read remains and write was removed, and the client may request a fresh read admission. Generic auth failures do not imply draft purge."}
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
