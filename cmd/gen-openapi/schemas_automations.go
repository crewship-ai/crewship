package main

// automationSchemaCatalog describes the /api/v1/automations surface — the
// workspace rules that turn a journal event into a deferred routine run.
//
// The shapes mirror internal/automation's wire types rather than the table:
// `matcher` and `action` are objects on the wire and TEXT columns in SQLite,
// and publishing the storage form would tell a client to send a JSON string
// where the handler expects an object.
func automationSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	arr := func(item map[string]any) map[string]any { return map[string]any{"type": "array", "items": item} }
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	matcher := obj(map[string]any{
		"crew_ids":    arr(str()),
		"agent_ids":   arr(str()),
		"mission_ids": arr(str()),
		"severities":  arr(str()),
		"payload_equals": map[string]any{
			"type": "object", "additionalProperties": true,
			"description": "Every pair must equal the corresponding journal payload field. Empty matches every entry of the event type. For an event type covered by the server's curated payload-key map, a key its emitter never writes is REJECTED at save time (400) naming the real keys — mission.status_change carries action, details, and (on a status transition) from and to. For an event type NOT covered by that map, a key the emitter never writes is accepted and matches nothing; POST /automations/preview names the offending clause either way.",
		},
	})
	action := obj(map[string]any{
		"routine_slug": str(),
		"inputs": map[string]any{
			"type": "object", "additionalProperties": true,
			"description": "Values may reference the triggering entry with {{ event.mission_id }}, {{ event.agent_id }}, {{ event.crew_id }}, {{ event.run_id }} and {{ event.payload.<key> }}.",
		},
	})
	automation := obj(map[string]any{
		"id":           str(),
		"workspace_id": str(),
		"name":         str(),
		"enabled":      boolean(),
		"event_type": map[string]any{"type": "string",
			"description": "A journal entry type, e.g. mission.status_change. Exactly one per rule. Checked at save time against the server's closed registry of every entry type actually declared or emitted; a value outside that registry is refused with 400, naming real alternatives."},
		"matcher":     matcher,
		"action_kind": map[string]any{"type": "string", "enum": []string{"routine"}},
		"action":      action,
		"debounce_seconds": map[string]any{"type": "integer",
			"description": "How long the enqueued run stays open for further matching events to coalesce into."},
		"max_per_hour": map[string]any{"type": "integer",
			"description": "Cap on runs this rule may cause per rolling hour. Over the cap, matches are dropped and one automation.throttled journal entry is written for the window."},
		"created_by": str(),
		"created_at": map[string]any{"type": "string", "format": "date-time"},
		"updated_at": map[string]any{"type": "string", "format": "date-time"},
	})
	// The write body. Every field is optional on both POST and PATCH: a
	// create fills the burst controls from the documented defaults, and a
	// patch is sparse so `automation disable` writes one field without
	// clobbering a matcher edited a moment earlier.
	writeBody := obj(map[string]any{
		"name": str(), "enabled": boolean(), "event_type": str(),
		"matcher": matcher, "action": action,
		"debounce_seconds": integer(), "max_per_hour": integer(),
	})

	return map[string]DomainSchema{
		"GET /api/v1/automations": {
			Response: obj(map[string]any{"automations": arr(automation), "count": integer()}),
		},
		"POST /api/v1/automations": {
			Request: writeBody, Response: automation,
		},
		"PATCH /api/v1/automations/{id}": {
			Request: writeBody, Response: automation,
		},
		"DELETE /api/v1/automations/{id}": {
			Response: obj(map[string]any{"status": str(), "id": str()}),
		},
		// The preview: a rule judged against history it has already seen,
		// without saving it and without starting a run. Either name a saved
		// rule or describe a candidate; the reply says what it WOULD have
		// caught, and when that is nothing, which clause is responsible.
		"POST /api/v1/automations/preview": {
			Request: obj(map[string]any{
				"automation_id": map[string]any{"type": "string",
					"description": "Preview a saved rule. Supply this OR event_type + matcher."},
				"event_type": map[string]any{"type": "string",
					"description": "Journal entry type to replay, for a rule that is not saved yet."},
				"matcher": matcher,
			}),
			Response: obj(map[string]any{
				"event_type":   str(),
				"window_hours": integer(),
				"scanned": map[string]any{"type": "integer",
					"description": "Entries of this event type in the window. Zero means there is nothing to judge the rule against — NOT that the rule is wrong."},
				"matched": integer(),
				"samples": arr(map[string]any{"type": "object", "additionalProperties": true}),
				"top_rejection": obj(map[string]any{
					"clause": map[string]any{"type": "string",
						"description": "The predicate that excluded the most entries, named in the matcher's own vocabulary."},
					"count":  integer(),
					"detail": str(),
					"key_absent": map[string]any{"type": "boolean",
						"description": "The predicate names a payload key the entry does not carry, so no change of value can make it match."},
				}),
			}),
		},
	}
}
