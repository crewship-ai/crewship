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
	conflict := obj(map[string]any{"sheet": str(), "cell": str(), "input": map[string]any{"type": "string", "description": "The cell's input in the head revision"}, "rev": integer(1), "actor_label": map[string]any{"type": "string", "description": "Display name of the writer of that input"}}, "sheet", "cell", "input", "rev", "actor_label")
	// Every error carries "error"; a 409 precondition_failed from an edit or
	// import also carries the head and what the client must do next.
	workbookError := obj(map[string]any{
		"error":             map[string]any{"type": "string", "description": "Machine-readable code, for example precondition_failed (409), editor_session_expired (409: open a new editor session; never a 401), snapshot_required (410), too_many_editor_sessions (429, with Retry-After) or gridcraft_editor_disabled (503)"},
		"head_rev":          map[string]any{"type": "integer", "minimum": 0, "description": "Current head revision (409 precondition_failed only)"},
		"snapshot_required": map[string]any{"type": "boolean", "description": "Reread the workbook; retrying the same packet cannot succeed (409 precondition_failed only)"},
		"conflicts":         map[string]any{"type": "array", "items": ref("WorkbookCellConflict"), "description": "Target cells another writer changed since base_rev (409 precondition_failed only)"},
		"generation":        map[string]any{"type": "integer", "minimum": 1, "description": "Current generation (410 snapshot_required only)"},
		"min_since":         map[string]any{"type": "integer", "minimum": 0, "description": "Oldest since the commits feed still serves (410 snapshot_required only)"},
	}, "error")
	sessionID := map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$"}
	openSession := obj(map[string]any{"session_id": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,128}$", "description": "Renew (heartbeat) this live session instead of opening a new one"}, "protocol": map[string]any{"type": "integer", "enum": []int{1}, "description": "Editor bridge protocol the client speaks"}})
	openSession["additionalProperties"] = false
	session := obj(map[string]any{"session_id": sessionID, "page_id": str(), "actor_label": map[string]any{"type": "string", "description": "Display name of the session's user"}, "rev": integer(1), "generation": integer(1), "protocol": map[string]any{"type": "integer", "enum": []int{1}}, "expires_at": map[string]any{"type": "string", "format": "date-time"}, "bundle_digest": map[string]any{"type": "string", "description": "Digest of the qualified editor bundle; empty when no editor origin is configured (do not frame)"}, "editor_url": map[string]any{"type": "string", "description": "Editor frame URL; absent when no editor origin is configured"}}, "session_id", "page_id", "actor_label", "rev", "generation", "protocol", "expires_at", "bundle_digest")
	feedOp := obj(map[string]any{"sheet": str(), "cell": str(), "input": str()}, "sheet", "cell", "input")
	commit := obj(map[string]any{
		"rev":         integer(1),
		"kind":        map[string]any{"type": "string", "enum": []string{"cells", "snapshot"}, "description": "snapshot is an import: reread the workbook"},
		"actor_label": map[string]any{"type": "string", "description": "Display name of the writer"},
		"own":         map[string]any{"type": "boolean", "description": "Written by the caller (for an agent, by its mandate)"},
		"session_id":  map[string]any{"type": "string", "description": "Own revisions only"},
		"mutation_id": map[string]any{"type": "string", "description": "Own revisions only"},
		"ops":         map[string]any{"type": "array", "items": feedOp},
	}, "rev", "kind", "actor_label", "own")
	commits := obj(map[string]any{"head_rev": integer(1), "generation": integer(1), "min_since": integer(0), "commits": map[string]any{"type": "array", "items": ref("WorkbookCommit")}}, "head_rev", "generation", "min_since", "commits")
	routes := map[string]DomainSchema{}
	routes["POST /api/v1/workbooks"] = DomainSchema{Request: create, RequestRequired: true, Response: created}
	base := "/api/v1/pages/{slug}/workbook"
	routes["GET "+base] = DomainSchema{Response: ref("WorkbookState")}
	routes["POST "+base+"/operations"] = DomainSchema{Request: request, RequestRequired: true, Response: ref("WorkbookReceipt")}
	routes["POST "+base+"/edits"] = DomainSchema{Request: edit, RequestRequired: true, Response: ref("WorkbookReceipt")}
	routes["GET "+base+"/operations/{mutation}"] = DomainSchema{Response: ref("WorkbookStatus")}
	routes["POST "+base+"/mandates"] = DomainSchema{Request: mandate, RequestRequired: true, Response: ref("WorkbookMandate")}
	routes["DELETE "+base+"/mandates/{mandate}"] = DomainSchema{Response: obj(map[string]any{"status": str()}, "status")}
	share := obj(map[string]any{"editor": map[string]any{"type": "string", "description": "Workspace member email or user ID"}, "publish": map[string]any{"type": "boolean", "description": "Required to end a private audience; publication is one-way and shares only the current head"}}, "editor")
	share["additionalProperties"] = false
	shared := obj(map[string]any{"page": str(), "editor": str(), "editor_user_id": str(), "published": map[string]any{"type": "boolean"}, "published_rev": integer(1), "revoked_mandates": integer(0)}, "page", "editor", "editor_user_id", "published", "revoked_mandates")
	routes["POST "+base+"/editors"] = DomainSchema{Request: share, RequestRequired: true, Response: shared}
	live := map[string]DomainSchema{
		"POST " + base + "/sessions":             {Request: openSession, Response: ref("WorkbookEditorSession"), SuccessStatuses: []string{"200", "201"}, ErrorStatuses: []string{"400", "401", "403", "404", "409", "429", "500", "503"}},
		"DELETE " + base + "/sessions/{session}": {Response: obj(map[string]any{"status": str(), "session_id": sessionID}, "status", "session_id"), ErrorStatuses: []string{"400", "401", "403", "404", "500", "503"}},
		"GET " + base + "/commits": {Response: ref("WorkbookCommits"), ErrorStatuses: []string{"400", "401", "403", "404", "410", "429", "500", "503"}, Parameters: []map[string]any{
			{"name": "since", "in": "query", "required": true, "description": "Return confirmed revisions after this one", "schema": integer(0)},
			{"name": "limit", "in": "query", "required": false, "description": "Revisions per page (default 100)", "schema": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}},
		}},
	}
	for key, c := range routes {
		c.SuccessStatuses = []string{"200"}
		if key == "POST /api/v1/workbooks" {
			c.SuccessStatuses = []string{"200", "201"}
		}
		c.ErrorStatuses = []string{"400", "401", "403", "404", "409", "429", "500", "503"}
		c.SuccessHeaders = map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
		if key == "POST "+base+"/edits" || key == "POST "+base+"/operations" {
			c.ErrorMedia, c.ErrorResponse = []string{"application/json"}, ref("WorkbookError")
		}
		routes[key] = c
	}
	for key, c := range live {
		if c.SuccessStatuses == nil {
			c.SuccessStatuses = []string{"200"}
		}
		c.SuccessHeaders = map[string]any{"Cache-Control": map[string]any{"schema": map[string]any{"type": "string", "enum": []string{"no-store"}}}}
		c.ErrorMedia, c.ErrorResponse = []string{"application/json"}, ref("WorkbookError")
		routes[key] = c
	}
	return routes, map[string]any{"WorkbookState": state, "WorkbookReceipt": receipt, "WorkbookStatus": status, "WorkbookMandate": mandate, "WorkbookError": workbookError, "WorkbookCellConflict": conflict, "WorkbookEditorSession": session, "WorkbookCommits": commits, "WorkbookCommit": commit}
}
