package main

func gridcraftSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	obj := func(p map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": p, "required": required}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func(min int) map[string]any { return map[string]any{"type": "integer", "minimum": min} }
	strings := map[string]any{"type": "array", "items": str()}
	snapshot := map[string]any{"type": "object", "description": "Native GridCraft workbook JSON, bounded and validated by the pinned offline engine."}
	key := obj(map[string]any{"artifact_id": str(), "authority_id": str(), "mutation_id": str()}, "artifact_id", "authority_id", "mutation_id")
	state := obj(map[string]any{"schema_version": integer(1), "rev": integer(1), "generation": integer(1), "snapshot": snapshot}, "schema_version", "rev", "generation", "snapshot")
	receipt := obj(map[string]any{"operation_key": key, "outcome": str(), "rev": integer(1), "payload_hash": str(), "issued_by": str(), "validated_by": str(), "effects": strings, "durability": str(), "at": str()}, "operation_key", "outcome", "rev", "payload_hash", "issued_by", "validated_by", "effects", "durability", "at")
	status := obj(map[string]any{"receipt": ref("WorkbookReceipt"), "state": str(), "head_rev": integer(0), "effect_in_head": str(), "delivery": str()}, "state", "head_rev", "effect_in_head", "delivery")
	command := obj(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"import"}}, "snapshot": snapshot}, "kind", "snapshot")
	request := obj(map[string]any{"mutation_id": str(), "mandate_id": map[string]any{"type": "string", "enum": []string{"human-import"}}, "base_rev": integer(0), "generation": integer(0), "command": command}, "mutation_id", "mandate_id", "base_rev", "generation", "command")
	request["additionalProperties"] = false
	command["additionalProperties"] = false
	mandate := obj(map[string]any{"mandate_id": str(), "run_id": str(), "base_rev": integer(1), "generation": integer(1), "targets": strings}, "mandate_id", "run_id", "base_rev", "generation", "targets")
	mandate["additionalProperties"] = false
	op := obj(map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"cell.set"}}, "sheet": str(), "cell": map[string]any{"type": "string", "pattern": "^[A-Z]{1,3}[1-9][0-9]{0,6}$"}, "input": map[string]any{"type": "string", "maxLength": 8192}}, "kind", "sheet", "cell", "input")
	op["additionalProperties"] = false
	edit := obj(map[string]any{"session_id": str(), "mutation_id": map[string]any{"type": "string", "description": "<session_id>:<seq>, stable across retries"}, "base_rev": integer(1), "generation": integer(1), "ops": map[string]any{"type": "array", "items": op, "minItems": 1, "maxItems": 64}}, "session_id", "mutation_id", "base_rev", "generation", "ops")
	edit["additionalProperties"] = false
	create := obj(map[string]any{"slug": str(), "name": str()}, "slug", "name")
	create["additionalProperties"] = false
	created := obj(map[string]any{"page_id": str(), "slug": str(), "name": str()}, "page_id", "slug", "name")
	routes := map[string]DomainSchema{}
	routes["POST /api/v1/workbooks"] = DomainSchema{Request: create, RequestRequired: true, Response: created}
	base := "/api/v1/pages/{slug}/workbook"
	routes["GET "+base] = DomainSchema{Response: ref("WorkbookState")}
	routes["POST "+base+"/operations"] = DomainSchema{Request: request, RequestRequired: true, Response: ref("WorkbookReceipt")}
	routes["POST "+base+"/edits"] = DomainSchema{Request: edit, RequestRequired: true, Response: ref("WorkbookReceipt")}
	routes["GET "+base+"/operations/{mutation}"] = DomainSchema{Response: ref("WorkbookStatus")}
	routes["POST "+base+"/mandates"] = DomainSchema{Request: mandate, RequestRequired: true, Response: ref("WorkbookMandate")}
	routes["DELETE "+base+"/mandates/{mandate}"] = DomainSchema{Response: obj(map[string]any{"status": str()}, "status")}
	for key, c := range routes {
		c.SuccessStatuses = []string{"200"}
		if key == "POST /api/v1/workbooks" {
			c.SuccessStatuses = []string{"200", "201"}
		}
		c.ErrorStatuses = []string{"400", "401", "403", "404", "409", "429", "500", "503"}
		c.SuccessHeaders = map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
		routes[key] = c
	}
	return routes, map[string]any{"WorkbookState": state, "WorkbookReceipt": receipt, "WorkbookStatus": status, "WorkbookMandate": mandate}
}
