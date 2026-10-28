package main

func craftNativeAgentSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	schemas := map[string]any{
		"NativeCraftAgentTaskRequest": obj(map[string]any{"chat_id": str(), "message_id": str(), "whole_document": map[string]any{"type": "boolean", "enum": []bool{true}}}, "chat_id", "message_id", "whole_document"),
		"NativeCraftAgentTaskReceipt": obj(map[string]any{"work_id": str(), "status": map[string]any{"type": "string", "enum": []string{"queued"}}}, "work_id", "status"),
		"NativeCraftAgentStopReceipt": obj(map[string]any{"grant_revoked": boolean(), "lease_retired": boolean(), "cancel_requested": boolean(), "stopped": boolean(), "stop_pending": boolean()}, "grant_revoked", "lease_retired", "cancel_requested", "stopped", "stop_pending"),
		"NativeCraftAgentToolEvent":   obj(map[string]any{"operation": map[string]any{"type": "string", "enum": []string{"read", "acquire", "renew", "apply", "release"}}, "status": map[string]any{"type": "integer"}, "at": map[string]any{"type": "string", "format": "date-time"}, "holder_kind": map[string]any{"type": "string", "enum": []string{"human", "agent", "unknown"}}, "receipt": ref("NativeCraftSnapshotReceipt")}, "operation", "status", "at"),
	}
	schemas["NativeCraftAgentTaskStatus"] = obj(map[string]any{"work_id": str(), "state": str(), "application": str(), "agent_id": str(), "run_id": str(), "grant_closed": boolean(), "operation_inflight": boolean(), "uncertain": boolean(), "timeline_durability": map[string]any{"type": "string", "enum": []string{"current-host-only"}}, "events": map[string]any{"type": "array", "maxItems": 64, "items": ref("NativeCraftAgentToolEvent")}, "last_committed_receipt": ref("NativeCraftSnapshotReceipt")}, "work_id", "state", "application", "agent_id", "run_id", "grant_closed", "operation_inflight", "uncertain", "timeline_durability", "events")
	routes := map[string]DomainSchema{
		"POST /api/v1/pages/{slug}/craft/tasks":         {Request: ref("NativeCraftAgentTaskRequest"), RequestRequired: true, Response: ref("NativeCraftAgentTaskReceipt"), SuccessStatuses: []string{"202"}},
		"POST /api/v1/pages/{slug}/craft/tasks/cancel":  {Request: ref("NativeCraftEmptyRequest"), RequestRequired: true, Response: ref("NativeCraftAgentStopReceipt"), SuccessStatuses: []string{"200", "202"}},
		"GET /api/v1/pages/{slug}/craft/tasks/{workID}": {NoRequest: true, Response: ref("NativeCraftAgentTaskStatus"), SuccessStatuses: []string{"200"}},
	}
	return routes, schemas
}
