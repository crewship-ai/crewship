package main

// Design uses standard Yjs updates on its websocket and typed JSON operations
// on the same Page authority. Historical reads for Read are shared-pin only;
// the default read is the live collaborative head.
func designSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	text := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	integer := func(min int) map[string]any {
		return map[string]any{"type": "integer", "format": "int64", "minimum": min}
	}
	array := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(props map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	id := func() map[string]any { return map[string]any{"type": "string", "minLength": 1, "maxLength": 128} }
	sharedPin := func() map[string]any {
		pin := integer(0)
		pin["description"] = "Version pinned by the owner for read-only callers; 0 while nothing is shared."
		return pin
	}
	geometry := func(size bool) map[string]any {
		minimum := -1000000
		if size {
			minimum = 0
		}
		return map[string]any{"type": "number", "minimum": minimum, "maximum": 1000000}
	}
	nodeProps := map[string]any{
		"id": id(), "type": map[string]any{"type": "string", "enum": []string{"frame", "rect", "text", "image"}},
		"name": str(), "parentId": str(), "x": geometry(false), "y": geometry(false), "width": geometry(true), "height": geometry(true),
		"fill": str(), "text": text(100000), "src": text(2000000),
		"linkTo": map[string]any{"type": "string", "maxLength": 128, "description": "Prototype link: ID of the screen (frame) a click on this layer opens in Play mode. Empty string clears it. A target that no longer exists is kept and plays as an inert hotspot."},
	}
	node := object(nodeProps, "id", "type", "x", "y", "width", "height")
	// The Yjs map may preserve extension fields; this is not a generic request
	// body fallback. All core node fields retain their concrete types above.
	node["additionalProperties"] = true
	patch := object(nodeProps, "id")
	patch["additionalProperties"] = true
	patch["description"] = "Partial fields merge into an existing Y.Map. A new node also requires type, x, y, width and height. The server validates the complete resulting document: at most 2000 nodes and 4 MiB of encoded Yjs state."
	operation := map[string]any{"oneOf": []any{
		object(map[string]any{"op": map[string]any{"type": "string", "enum": []string{"upsert"}}, "node": ref("DesignNodePatch")}, "op", "node"),
		object(map[string]any{"op": map[string]any{"type": "string", "enum": []string{"delete"}}, "id": str()}, "op", "id"),
		object(map[string]any{"op": map[string]any{"type": "string", "enum": []string{"reorder"}}, "order": map[string]any{"type": "array", "maxItems": 4000, "items": str()}}, "op", "order"),
	}}
	schemas := map[string]any{
		"DesignNode": node, "DesignNodePatch": patch, "DesignOperation": operation,
		"DesignActor":              object(map[string]any{"id": str(), "name": str()}, "id", "name"),
		"DesignCreateRequest":      object(map[string]any{"created_by_test": map[string]any{"type": "boolean"}, "slug": map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{0,63}$"}, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "source_page_id": text(128), "expected_human": str(), "initial_state": map[string]any{"type": "string", "format": "byte", "maxLength": 5592408, "description": "Optional recovery copy, at most 4 MiB decoded Yjs. Requires source_page_id and expected_human together, fresh human source Edit and Page create authority."}}, "slug", "name"),
		"DesignCreateResponse":     object(map[string]any{"page_id": str(), "slug": str(), "version": integer(1)}, "page_id", "slug", "version"),
		"DesignSnapshot":           object(map[string]any{"page_id": str(), "authority_revision": map[string]any{"type": "string", "description": "Opaque workspace policy revision. Keep as a string; a fresh authorized metadata revision establishes a replacement collaborative session."}, "origin": map[string]any{"oneOf": []any{ref("ArtifactProjectOrigin"), map[string]any{"type": "null"}}}, "can_edit": map[string]any{"type": "boolean"}, "can_comment": map[string]any{"type": "boolean"}, "nodes": array(ref("DesignNode")), "order": array(str()), "version": integer(1), "latest_version": integer(1), "shared_version": sharedPin(), "actor": ref("DesignActor")}, "page_id", "authority_revision", "origin", "can_edit", "can_comment", "nodes", "order", "version", "latest_version", "shared_version", "actor"),
		"DesignOperationsRequest":  object(map[string]any{"operations": map[string]any{"type": "array", "maxItems": 500, "items": ref("DesignOperation")}, "message": text(4096)}, "operations"),
		"DesignOperationsResponse": object(map[string]any{"nodes": array(ref("DesignNode")), "order": array(str()), "version": integer(1)}, "nodes", "order", "version"),
		"DesignCheckpointRequest":  object(map[string]any{"message": text(4096)}),
		"DesignVersionRequest":     object(map[string]any{"version": integer(1)}, "version"),
		"DesignVersionReceipt":     object(map[string]any{"version": integer(1), "actor": str()}, "version", "actor"),
		"DesignVersion":            object(map[string]any{"version": integer(1), "entry_point": str(), "message": text(4096), "actor": str(), "created_at": map[string]any{"type": "string", "format": "date-time"}, "restored_from": integer(1)}, "version", "entry_point", "actor", "created_at"),
		"DesignVersionList":        object(map[string]any{"versions": array(ref("DesignVersion")), "latest_version": integer(1), "shared_version": sharedPin()}, "versions", "latest_version", "shared_version"),
		"DesignCommentsResponse":   object(map[string]any{"comments": array(ref("ArtifactProjectComment"))}, "comments"),
		"DesignCommentRequest":     object(map[string]any{"version": integer(1), "path": map[string]any{"type": "string", "enum": []string{"design.json"}}, "selector": id(), "quote": text(2000), "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 10000}, "send_to_agent": map[string]any{"type": "boolean"}}, "version", "path", "selector", "text"),
		"DesignShareResponse":      object(map[string]any{"version": integer(1), "shared_version": integer(1)}, "version", "shared_version"),
		"DesignAgentRequest":       object(map[string]any{"chat_id": map[string]any{"type": "string", "minLength": 1}}, "chat_id"),
		"DesignAgentResponse":      object(map[string]any{"origin": ref("ArtifactProjectOrigin")}, "origin"),
	}
	routes := map[string]DomainSchema{}
	add := func(method, path, request, response, status string) {
		schema := DomainSchema{Response: ref(response), SuccessStatuses: []string{status}, ErrorResponse: ref("ArtifactProjectError"), ErrorMedia: []string{"application/json"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "413", "500", "503"}, Parameters: []map[string]any{{"name": "workspace_id", "in": "query", "required": false, "description": "Workspace ID or slug; the workspace header is also accepted.", "schema": str()}}}
		if request != "" {
			schema.Request = ref(request)
			schema.RequestRequired = true
		} else {
			schema.NoRequest = true
		}
		routes[method+" "+path] = schema
	}
	const prefix = "/api/v1/pages/{slug}/design"
	add("POST", "/api/v1/pages/design", "DesignCreateRequest", "DesignCreateResponse", "201")
	add("GET", prefix, "", "DesignSnapshot", "200")
	add("GET", prefix+"/versions", "", "DesignVersionList", "200")
	add("POST", prefix+"/operations", "DesignOperationsRequest", "DesignOperationsResponse", "200")
	add("POST", prefix+"/snapshots", "DesignCheckpointRequest", "DesignVersionReceipt", "200")
	add("POST", prefix+"/restore", "DesignVersionRequest", "DesignVersionReceipt", "200")
	add("GET", prefix+"/comments", "", "DesignCommentsResponse", "200")
	add("POST", prefix+"/comments", "DesignCommentRequest", "ArtifactProjectComment", "201")
	add("POST", prefix+"/share", "DesignVersionRequest", "DesignShareResponse", "200")
	add("POST", prefix+"/agent", "DesignAgentRequest", "DesignAgentResponse", "200")
	get := routes["GET "+prefix]
	get.Parameters = append(get.Parameters, map[string]any{"name": "version", "in": "query", "required": false, "schema": integer(1), "description": "Historical checkpoint. Ordinary Read/Comment audiences may select only the shared pin. Workspace ADMIN/OWNER retain full history with read-scoped credentials; omit for the latest authorized canvas."})
	routes["GET "+prefix] = get
	ws := DomainSchema{NoRequest: true, SuccessStatuses: []string{"101"}, ErrorResponse: ref("ArtifactProjectError"), ErrorStatuses: []string{"400", "401", "403", "404", "500", "503"}, ErrorMedia: []string{"application/json"}, Parameters: []map[string]any{{"name": "workspace_id", "in": "query", "required": false, "schema": str()}}, SuccessHeaders: map[string]any{"Upgrade": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"websocket"}}, "description": "Binary standard y-protocols sync and awareness frames follow the upgrade; Read sends only syncStep1 and awareness. Edit may send updates. Rights are rechecked for every delivery and write."}}}
	ws.Parameters = append(ws.Parameters, map[string]any{"name": "authority_revision", "in": "query", "required": false, "schema": str(), "description": "Expected opaque policy revision from fresh authorized Design metadata. A mismatch is rejected before upgrade; browser renewal always supplies it."})
	routes["GET "+prefix+"/ws"] = ws
	return routes, schemas
}
