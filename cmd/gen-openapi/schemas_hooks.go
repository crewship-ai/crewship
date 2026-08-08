package main

// hookSchemaCatalog describes the /api/v1/hooks write surface — the
// lifecycle intercepts that can veto a tool call, an agent start or an
// approval before it happens.
//
// Without these, gen-openapi infers `{"type": "object"}` for both the body
// and the reply, and `docs-inventory -strict` fails the routes on exactly
// that: a generic schema documents the existence of an endpoint while
// telling a client nothing about how to call it. The signals surface hit
// the same gate; this is the same fix in the same shape.
//
// The shapes mirror internal/api's wire types (hookRow in hooks_handler.go,
// the write body in hooks_write.go), NOT the hooks_config table: `matcher`
// and `handler_config` are objects on the wire and TEXT columns in SQLite,
// and publishing the storage form would tell a client to send a JSON string
// where the handler expects an object.
func hookSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}
	dt := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }

	// internal/hooks.Matcher. `when` is declared and deliberately ignored
	// today — it reserves the slot for a future expression predicate, and
	// documenting it as accepted-but-inert is more honest than hiding it.
	matcher := obj(map[string]any{
		"tools":      arr(str()),
		"agent_ids":  arr(str()),
		"crew_ids":   arr(str()),
		"severities": arr(str()),
		"when": map[string]any{"type": "string",
			"description": "Reserved for a future expression predicate. Accepted and ignored today."},
	})

	handlerKind := map[string]any{
		"type": "string", "enum": []string{"shell", "http", "subagent"},
		"description": "shell runs a command on the HOST and is OWNER-only, on create and on any edit that leaves a hook shell.",
	}

	hook := obj(map[string]any{
		"id":             str(),
		"workspace_id":   str(),
		"crew_id":        str(),
		"event":          map[string]any{"type": "string", "description": "One of the declared lifecycle events; an unknown value is rejected and the error lists the valid ones."},
		"handler_kind":   handlerKind,
		"handler_config": map[string]any{"type": "object", "additionalProperties": true},
		"matcher":        matcher,
		"enabled":        boolean(),
		"blocking":       map[string]any{"type": "boolean", "description": "A blocking hook runs synchronously and may veto the intercepted action."},
		"created_by":     str(),
		"created_at":     dt(),
		"updated_at":     dt(),
	})

	// One body for both verbs. Every field is a pointer in the handler, so
	// PATCH is sparse: omitted fields keep their stored value rather than
	// being cleared.
	writeBody := obj(map[string]any{
		"crew_id":        str(),
		"event":          str(),
		"matcher":        matcher,
		"handler_kind":   handlerKind,
		"handler_config": map[string]any{"type": "object", "additionalProperties": true},
		"blocking":       boolean(),
		"enabled":        boolean(),
	})

	return map[string]DomainSchema{
		"POST /api/v1/hooks": {
			Request: writeBody, Response: hook,
		},
		"PATCH /api/v1/hooks/{id}": {
			Request: writeBody, Response: hook,
		},
		"DELETE /api/v1/hooks/{id}": {
			Response: obj(map[string]any{"id": str(), "deleted": boolean()}),
		},
	}
}
