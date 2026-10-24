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
	base := "/api/v1/pages/{slug}/mooring"
	routes := map[string]DomainSchema{"GET /api/v1/mooring/apps": {Response: ref("MooringApplications")}, "POST /api/v1/mooring/documents": {Request: ref("MooringCreate"), RequestRequired: true, Response: ref("MooringCreated"), SuccessStatuses: []string{"200", "201"}}, "GET " + base: {Response: ref("MooringRead")}, "POST " + base + "/commands": {Request: ref("MooringCommand"), RequestRequired: true, Response: ref("MooringReceipt")}, "POST " + base + "/mandates": {Request: ref("MooringMandate"), RequestRequired: true, Response: ref("MooringMandate")}, "DELETE " + base + "/mandates/{mandate}": {Response: ref("MooringRevoked")}}
	for path, r := range routes {
		if r.SuccessStatuses == nil {
			r.SuccessStatuses = []string{"200"}
		}
		r.ErrorStatuses = []string{"400", "401", "403", "404", "409", "500", "503"}
		routes[path] = r
	}
	return routes, schemas
}
