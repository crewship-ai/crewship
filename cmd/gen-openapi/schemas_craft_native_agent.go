package main

func craftNativeAgentSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	decimal := func() map[string]any {
		return map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$", "maxLength": 19, "description": "Canonical positive int64 decimal string; range checked by the host."}
	}
	command := obj(map[string]any{"command": map[string]any{"type": "string", "enum": []string{"caret.docStart", "caret.docEnd", "text.insert", "cell.set", "text", "cell.get"}}, "params": map[string]any{"type": "object", "description": "Closed original-engine parameter allowlist: caret {}, text.insert {text}, cell.set {cell,input} scalar literals, text {}, or cell.get {cell}."}}, "command", "params")
	schemas := map[string]any{
		"NativeCraftAgentTaskRequest":  obj(map[string]any{"chat_id": str(), "message_id": str(), "whole_document": map[string]any{"type": "boolean", "enum": []bool{true}}}, "chat_id", "message_id", "whole_document"),
		"NativeCraftAgentTaskReceipt":  obj(map[string]any{"work_id": str(), "status": map[string]any{"type": "string", "enum": []string{"queued"}}}, "work_id", "status"),
		"NativeCraftAgentStopReceipt":  obj(map[string]any{"grant_revoked": boolean(), "lease_retired": boolean(), "cancel_requested": boolean(), "stopped": boolean(), "stop_pending": boolean()}, "grant_revoked", "lease_retired", "cancel_requested", "stopped", "stop_pending"),
		"NativeCraftAgentLeaseReceipt": obj(map[string]any{"generation": decimal(), "expires_at": map[string]any{"type": "string", "format": "date-time"}, "holder_kind": map[string]any{"type": "string", "enum": []string{"agent"}}}, "generation", "expires_at", "holder_kind"),
		"NativeCraftAgentReadReceipt":  obj(map[string]any{"application": map[string]any{"type": "string", "enum": []string{"wordcraft", "gridcraft"}}, "version": decimal(), "engine_pin": str(), "view": map[string]any{"type": "object", "description": "Bounded original-engine query result; no arbitrary native document export."}}, "application", "version", "engine_pin"),
		"NativeCraftAgentToolEvent":    obj(map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"read", "acquire", "renew", "apply", "release"}}, "status": map[string]any{"type": "integer"}, "at": map[string]any{"type": "string", "format": "date-time"}, "holder_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}, "receipt": ref("NativeCraftSnapshotReceipt")}, "operation", "status", "at"),
	}
	schemas["NativeCraftAgentTaskStatus"] = obj(map[string]any{"work_id": str(), "state": str(), "application": str(), "agent_id": str(), "run_id": str(), "grant_closed": boolean(), "operation_inflight": boolean(), "uncertain": boolean(), "timeline_durability": map[string]any{"type": "string", "enum": []string{"current-host-only"}}, "events": map[string]any{"type": "array", "maxItems": 64, "items": ref("NativeCraftAgentToolEvent")}, "last_committed_receipt": ref("NativeCraftSnapshotReceipt")}, "work_id", "state", "application", "agent_id", "run_id", "grant_closed", "operation_inflight", "uncertain", "timeline_durability", "events")
	variants := []any{}
	for _, op := range []string{"read", "acquire", "renew", "apply", "release"} {
		props := map[string]any{"operation": map[string]any{"type": "string", "enum": []string{op}}}
		required := []string{"operation"}
		if op == "read" {
			props["query"] = command
		}
		if op == "renew" || op == "apply" || op == "release" {
			props["generation"] = decimal()
			required = append(required, "generation")
		}
		if op == "apply" {
			props["base_version"] = decimal()
			props["request_id"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "pattern": "^[A-Za-z0-9_.:-]+$"}
			props["commands"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": command}
			required = append(required, "base_version", "request_id", "commands")
		}
		variants = append(variants, obj(props, required...))
	}
	schemas["NativeCraftAgentToolRequest"] = map[string]any{"oneOf": variants, "description": "Fixed run-bound RPC. No Page, actor, run, profile or executable selectors. Repeating an apply can reconcile only the exact original request; it never reruns the engine."}
	routes := map[string]DomainSchema{
		"POST /api/v1/pages/{slug}/craft/tasks":         {Request: ref("NativeCraftAgentTaskRequest"), RequestRequired: true, Response: ref("NativeCraftAgentTaskReceipt"), SuccessStatuses: []string{"202"}},
		"POST /api/v1/pages/{slug}/craft/tasks/cancel":  {Request: ref("NativeCraftEmptyRequest"), RequestRequired: true, Response: ref("NativeCraftAgentStopReceipt"), SuccessStatuses: []string{"200", "202"}},
		"GET /api/v1/pages/{slug}/craft/tasks/{workID}": {NoRequest: true, Response: ref("NativeCraftAgentTaskStatus"), SuccessStatuses: []string{"200"}},
		"POST /api/v1/internal/craft-native/task":       {Request: ref("NativeCraftAgentToolRequest"), RequestRequired: true, Response: map[string]any{"oneOf": []any{ref("NativeCraftSnapshotReceipt"), ref("NativeCraftAgentReadReceipt"), ref("NativeCraftAgentLeaseReceipt"), obj(map[string]any{"released": boolean()}, "released")}}, SuccessStatuses: []string{"200"}, Parameters: []map[string]any{{"name": "X-Internal-Token", "in": "header", "required": true, "schema": str()}, {"name": "Authorization", "in": "header", "required": true, "description": "Actual agtv2 run credential plus a live host-retained confirmed native task mandate.", "schema": str()}}, ErrorStatuses: []string{"400", "403", "409", "413", "422", "503"}},
	}
	return routes, schemas
}
