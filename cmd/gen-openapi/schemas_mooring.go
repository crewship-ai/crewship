package main

func mooringSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	obj := func(p map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false}
	}
	text := map[string]any{"type": "string"}
	positive := map[string]any{"type": "integer", "minimum": 1}
	value := map[string]any{"description": "Canonical JSON validated by the installed application's command schema"}
	editor := obj(map[string]any{"entry": text}, "entry")
	command := obj(map[string]any{"name": text, "effect": text, "input_schema": map[string]any{"type": "object", "description": "Installed JSON Schema; cannot be supplied by document content"}}, "name", "effect", "input_schema")
	manifest := obj(map[string]any{"id": text, "protocol_version": text, "format": text, "editor": editor, "initial_state": value, "commands": map[string]any{"type": "array", "items": command}}, "id", "protocol_version", "format", "editor", "initial_state", "commands")
	state := obj(map[string]any{"rev": positive, "generation": positive, "snapshot": value}, "rev", "generation", "snapshot")
	receipt := obj(map[string]any{"mutation_id": text, "rev": positive, "outcome": text, "issued_by": text, "durability": text, "visibility": text, "payload_hash": text, "at": text}, "mutation_id", "rev", "outcome", "issued_by", "durability", "visibility", "payload_hash", "at")
	create := obj(map[string]any{"app_id": text, "slug": text, "name": text}, "app_id", "slug", "name")
	created := obj(map[string]any{"page_id": text, "app_id": text, "slug": text, "name": text}, "page_id", "app_id", "slug", "name")
	read := obj(map[string]any{"app_id": text, "manifest": ref("MooringManifest"), "state": ref("MooringState"), "editor": obj(map[string]any{"url": text, "bundle_digest": text})}, "app_id", "manifest", "state", "editor")
	request := obj(map[string]any{"mutation_id": text, "base_rev": positive, "generation": positive, "command": text, "arguments": value}, "mutation_id", "base_rev", "generation", "command", "arguments")
	mandate := obj(map[string]any{"mandate_id": text, "run_id": text, "base_rev": positive, "generation": positive, "command": text}, "mandate_id", "run_id", "base_rev", "generation", "command")
	schemas := map[string]any{"MooringManifest": manifest, "MooringApplications": map[string]any{"type": "array", "items": ref("MooringManifest")}, "MooringState": state, "MooringReceipt": receipt, "MooringCreate": create, "MooringCreated": created, "MooringRead": read, "MooringCommand": request, "MooringMandate": mandate, "MooringRevoked": obj(map[string]any{"status": text}, "status")}
	// These are the human session handler's closed wire shapes. Authority is
	// derived from the authenticated Page request, never supplied in this body.
	versions := obj(map[string]any{
		"protocol_version": map[string]any{"type": "string", "enum": []string{"0.4.0"}},
		"model_version":    map[string]any{"type": "string", "enum": []string{"0.2.0"}},
		"engine_version":   map[string]any{"type": "string", "enum": []string{"0.10.0"}},
	}, "protocol_version", "model_version", "engine_version")
	schemas["MooringSessionRequest"] = obj(map[string]any{
		"write":             map[string]any{"type": "boolean", "nullable": true, "default": false},
		"checkpoint_base64": map[string]any{"type": "string", "nullable": true, "description": "Optional canonical base64 initialization checkpoint; decoded size at most 1536 KiB. The whole request is limited to 2 MiB."},
	})
	// No required properties: {} is a valid read-only session request.
	delete(schemas["MooringSessionRequest"].(map[string]any), "required")
	schemas["MooringSession"] = obj(map[string]any{
		"session_id": text, "checkpoint_revision": map[string]any{"type": "string", "pattern": "^(0|[1-9][0-9]*)$"},
		"sdk_prefix": text, "participant_ref": text, "ticket": text, "connection_ref": text,
		"origin":   map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$", "description": "Host-reserved positive origin, at most 2^53-1; decimal string."},
		"versions": versions, "websocket_path": text,
		"editor": obj(map[string]any{"url": text, "bundle_digest": text}, "url", "bundle_digest"),
	}, "session_id", "checkpoint_revision", "sdk_prefix", "participant_ref", "ticket", "connection_ref", "origin", "versions", "websocket_path")
	base := "/api/v1/pages/{slug}/mooring"
	routes := map[string]DomainSchema{"GET /api/v1/mooring/apps": {Response: ref("MooringApplications")}, "POST /api/v1/mooring/documents": {Request: ref("MooringCreate"), RequestRequired: true, Response: ref("MooringCreated"), SuccessStatuses: []string{"200", "201"}}, "GET " + base: {Response: ref("MooringRead")}, "POST " + base + "/commands": {Request: ref("MooringCommand"), RequestRequired: true, Response: ref("MooringReceipt")}, "POST " + base + "/mandates": {Request: ref("MooringMandate"), RequestRequired: true, Response: ref("MooringMandate")}, "DELETE " + base + "/mandates/{mandate}": {Response: ref("MooringRevoked")}}
	routes["POST "+base+"/session"] = DomainSchema{
		Request: ref("MooringSessionRequest"), RequestRequired: true, Response: ref("MooringSession"),
		ErrorStatuses: []string{"400", "401", "403", "413", "500", "503"},
	}
	routes["GET "+base+"/ws"] = DomainSchema{
		NoRequest: true, SuccessStatuses: []string{"101"},
		Parameters: []map[string]any{{"name": "connection_ref", "in": "query", "required": true,
			"schema":      map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "pattern": "^[^\\u0000]+$"},
			"description": "Original connection_ref returned by the authenticated session request; a connection can be used only once."}},
		SuccessHeaders: map[string]any{
			"Upgrade":    map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"websocket"}}},
			"Connection": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"Upgrade"}}},
		},
		ErrorStatuses: []string{"400", "401", "403", "500", "503"},
	}
	for path, r := range routes {
		if r.SuccessStatuses == nil {
			r.SuccessStatuses = []string{"200"}
		}
		if r.ErrorStatuses == nil {
			r.ErrorStatuses = []string{"400", "401", "403", "404", "409", "500", "503"}
		}
		routes[path] = r
	}
	return routes, schemas
}
