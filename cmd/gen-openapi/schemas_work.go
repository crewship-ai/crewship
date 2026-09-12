package main

// workLedgerSchemaCatalog documents the durable work ledger's read surface and
// its operator actions (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md
// §9).
//
// Spelled out rather than left to the generic envelope for two reasons. The
// first is the ordinary one: a client has to be able to read from the spec that
// `state` has exactly ten values and which four of them are terminal. The
// second is specific to this surface — the cancel response's `outcome` is the
// field that distinguishes "we stopped it" from "we asked, and it may still be
// running", and a generic object schema would let a UI treat the two as the
// same 200.
//
// What is NOT here is as deliberate: no `input_json` on a work item, no
// `detail_json` on an event, no `raw_body` on a delivery. The handlers do not
// serialize them (§9 forbids conversation text, credentials and LLM output in
// these responses), and a schema that advertised them would be a promise the
// server has no intention of keeping.
func workLedgerSchemaCatalog() (map[string]DomainSchema, map[string]any) {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	nullable := func(s map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range s {
			out[k] = v
		}
		out["nullable"] = true
		return out
	}
	object := func(p map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": p}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }
	enum := func(values ...string) map[string]any {
		vals := make([]any, 0, len(values))
		for _, v := range values {
			vals = append(vals, v)
		}
		return map[string]any{"type": "string", "enum": vals}
	}

	// The lifecycle from §4. succeeded / failed / expired / cancelled are
	// terminal; needs_reconciliation deliberately is not — it holds its
	// conflicting capacity until somebody resolves it.
	state := func() map[string]any {
		return enum("queued", "starting", "running", "waiting", "retry_wait",
			"succeeded", "failed", "expired", "cancelled", "needs_reconciliation")
	}

	workItemProperties := func() map[string]any {
		return map[string]any{
			"id":           str(),
			"workspace_id": str(),
			"source":       enum("webhook", "chat", "assignment", "schedule", "pipeline_step", "manual"),
			// The delivery / mailbox message / schedule fire this work stands for.
			"source_ref":  str(),
			"domain_kind": str(),
			"domain_id":   str(),
			"agent_id":    str(),
			"crew_id":     str(),
			"session_id":  str(),
			"class":       enum("chat", "background"),
			// The authorization that admitted it. Re-checked at dispatch, so a
			// permission removed while the work waited takes effect then.
			"authorized_by_user_id": str(),
			// The input's fingerprint. The input itself is never returned.
			"input_sha256":    str(),
			"target_revision": str(),
			"state":           state(),
			"state_reason":    str(),
			// Bumped on every claim; the fencing term a transition carries.
			"generation":    integer(),
			"attempt_count": integer(),
			"priority":      integer(),
			"eligible_at":   dateTime(),
			// null means the work never expires on its own.
			"deadline_at": nullable(dateTime()),
			// Set when this item is a manual replay of another.
			"replay_of":     nullable(str()),
			"replay_reason": str(),
			"created_at":    dateTime(),
			"updated_at":    dateTime(),
			"terminal_at":   nullable(dateTime()),
		}
	}
	workItemRequired := []string{
		"id", "workspace_id", "source", "source_ref", "domain_kind", "domain_id",
		"agent_id", "crew_id", "session_id", "class", "authorized_by_user_id",
		"input_sha256", "target_revision", "state", "state_reason", "generation",
		"attempt_count", "priority", "eligible_at", "deadline_at", "replay_of",
		"replay_reason", "created_at", "updated_at", "terminal_at",
	}

	// The detail is the summary plus the two lists. The summary's counter is
	// `attempt_count` and the detail's list is `attempts`, deliberately not the
	// same name: one shape saying "3" and another saying "[…]" under one key is
	// how a client ends up branching on typeof.
	detailProperties := workItemProperties()
	detailProperties["attempts"] = map[string]any{"type": "array", "items": ref("WorkAttempt")}
	detailProperties["events"] = map[string]any{"type": "array", "items": ref("WorkEvent")}
	detailRequired := append(append([]string{}, workItemRequired...), "attempts", "events")

	components := map[string]any{
		"WorkItem":       object(workItemProperties(), workItemRequired...),
		"WorkItemDetail": object(detailProperties, detailRequired...),
		"WorkAttempt": object(map[string]any{
			"run_id":     str(),
			"attempt":    integer(),
			"generation": integer(),
			"lease_owner": map[string]any{"type": "string",
				"description": "Which dispatcher holds this attempt. A transition carrying a stale generation is refused regardless."},
			"lease_expires_at": dateTime(),
			"heartbeat_at":     dateTime(),
			"runtime_locator": map[string]any{"type": "string",
				"description": "Where the runtime is. Recovery consults it before deciding a process is gone; an expired lease alone does not authorize a second one."},
			"started_at":    dateTime(),
			"start_reason":  str(),
			"ended_at":      nullable(dateTime()),
			"end_reason":    str(),
			"exit_evidence": str(),
			"cost_usd":      number(),
		}, "run_id", "attempt", "generation", "lease_owner", "lease_expires_at",
			"heartbeat_at", "runtime_locator", "started_at", "start_reason",
			"ended_at", "end_reason", "exit_evidence", "cost_usd"),
		"WorkEvent": object(map[string]any{
			"seq": integer(),
			"at":  dateTime(),
			// from_state equals to_state on a recorded cancel REQUEST: it is a
			// durable record of an operator asking, not a transition.
			"from_state": str(),
			"to_state":   state(),
			"run_id":     str(),
			"generation": integer(),
			"reason":     str(),
		}, "seq", "at", "from_state", "to_state", "run_id", "generation", "reason"),
		"WorkItemPage": object(map[string]any{
			"items":       map[string]any{"type": "array", "items": ref("WorkItem"), "maxItems": 100},
			"next_cursor": nullable(str()),
		}, "items", "next_cursor"),
		"WorkCancelRequest": object(map[string]any{}),
		"WorkCancelResponse": object(map[string]any{
			"id": str(),
			"state": map[string]any{"type": "string",
				"enum": state()["enum"],
				"description": "The item's REAL state after the call. A cancel that lost the race to a completion " +
					"reports the finished state rather than claiming a cancel it did not perform."},
			"outcome": map[string]any{"type": "string",
				"enum": []any{"cancelled", "requested", "already_terminal"},
				"description": "cancelled: queued or retrying work stopped atomically. requested: the work holds a " +
					"runtime, the request is recorded and the signal follows; `cancelled` only after a confirmed stop. " +
					"already_terminal: it finished first."},
			"detail": str(),
		}, "id", "state", "outcome", "detail"),
		"WorkResolveRequest": object(map[string]any{
			"generation":      map[string]any{"type": "integer", "minimum": 1},
			"state":           enum("succeeded", "failed", "cancelled"),
			"runtime_stopped": map[string]any{"type": "boolean", "enum": []any{true}, "description": "Operator attests runtime has stopped; this endpoint does not stop it."},
			"reason":          map[string]any{"type": "string", "minLength": 1, "maxLength": 2048},
		}, "generation", "state", "runtime_stopped", "reason"),
		"WorkReplayRequest": object(map[string]any{
			"reason": map[string]any{"type": "string", "maxLength": 2000,
				"description": "Why this is being replayed. Recorded on the new work item."},
			"target_revision": map[string]any{"type": "string",
				"description": "Replay against a different target revision. Omitted inherits the original's — §4 requires a different revision to be explicit."},
		}),
		"WebhookDelivery": object(map[string]any{
			"id":            str(),
			"workspace_id":  str(),
			"endpoint_id":   str(),
			"endpoint_kind": enum("agent", "routine", "page"),
			"profile": map[string]any{"type": "string",
				"description": "The signature profile that verified this delivery — recorded, never inferred from the presence of a header."},
			"source_delivery_id": map[string]any{"type": "string",
				"description": "The sender's own identifier. Identity is (workspace, endpoint, source delivery id) and never the signature."},
			"event_type":     str(),
			"event_action":   str(),
			"signing_key_id": str(),
			"body_sha256":    str(),
			"body_bytes":     integer(),
			"filter_decision": map[string]any{"type": "string", "enum": []any{"accepted", "ignored"},
				"description": "An ignored delivery is audited rather than dropped, so a ping or a filtered event stays recoverable."},
			"filter_reason":   str(),
			"target_revision": str(),
			"work_id":         nullable(str()),
			"received_at":     dateTime(),
			"dedup_expires_at": map[string]any{"type": "string", "format": "date-time",
				"description": "Until when a re-delivery of this id returns the original receipt instead of creating new work."},
			"raw_body_available": map[string]any{"type": boolean()["type"],
				"description": "Whether the payload is still held. False means a replay of the work this produced is unavailable."},
			"raw_body_expires_at": nullable(dateTime()),
		}, "id", "workspace_id", "endpoint_id", "endpoint_kind", "profile",
			"source_delivery_id", "event_type", "event_action", "signing_key_id",
			"body_sha256", "body_bytes", "filter_decision", "filter_reason",
			"target_revision", "work_id", "received_at", "dedup_expires_at",
			"raw_body_available", "raw_body_expires_at"),
		"WebhookDeliveryPage": object(map[string]any{
			"items":       map[string]any{"type": "array", "items": ref("WebhookDelivery"), "maxItems": 100},
			"next_cursor": nullable(str()),
		}, "items", "next_cursor"),
	}
	// Neither POST accepts anything beyond what is named. A silently-ignored
	// `target_revision` typo would run the replay against the wrong revision
	// and report success.
	components["WorkCancelRequest"].(map[string]any)["additionalProperties"] = false
	components["WorkCancelRequest"].(map[string]any)["description"] =
		"No body. Cancel is an idempotent request keyed by the work item in the path."
	components["WorkReplayRequest"].(map[string]any)["additionalProperties"] = false
	components["WorkResolveRequest"].(map[string]any)["additionalProperties"] = false

	routes := map[string]DomainSchema{
		"GET /api/v1/workspaces/{workspaceId}/work-items":                       {Response: ref("WorkItemPage")},
		"GET /api/v1/workspaces/{workspaceId}/work-items/{workItemId}":          {Response: ref("WorkItemDetail")},
		"POST /api/v1/workspaces/{workspaceId}/work-items/{workItemId}/cancel":  {Request: ref("WorkCancelRequest"), Response: ref("WorkCancelResponse")},
		"POST /api/v1/workspaces/{workspaceId}/work-items/{workItemId}/resolve": {Request: ref("WorkResolveRequest"), Response: ref("WorkItem")},
		"POST /api/v1/workspaces/{workspaceId}/work-items/{workItemId}/replay":  {Request: ref("WorkReplayRequest"), Response: ref("WorkItem"), SuccessStatuses: []string{"201"}},
		"GET /api/v1/workspaces/{workspaceId}/webhook-deliveries":               {Response: ref("WebhookDeliveryPage")},
		"GET /api/v1/workspaces/{workspaceId}/webhook-deliveries/{deliveryId}":  {Response: ref("WebhookDelivery")},
	}
	return routes, components
}
