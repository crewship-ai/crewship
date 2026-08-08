package main

// observabilityPaymentsSchemaCatalog contains the handler-audited contracts
// for the journal, cost, metrics, flags, presence, runtime and notification
// surfaces. It is intentionally a separate catalog so parallel schema work
// can be merged without editing another agent's domain files.
func observabilityPaymentsSchemaCatalog() map[string]DomainSchema {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	integer := func() map[string]any { return map[string]any{"type": "integer"} }
	number := func() map[string]any { return map[string]any{"type": "number"} }
	boolean := func() map[string]any { return map[string]any{"type": "boolean"} }
	dateTime := func() map[string]any { return map[string]any{"type": "string", "format": "date-time"} }
	nullableString := func() map[string]any { return map[string]any{"type": "string", "nullable": true} }
	object := func(properties map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": properties}
	}
	freeObject := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	stringMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": str()}
	}
	array := func(item map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": item}
	}

	journalEntry := object(map[string]any{
		"id": str(), "workspace_id": str(), "ts": dateTime(), "entry_type": str(),
		"severity": str(), "priority": str(), "actor_type": str(), "summary": str(),
		"crew_id": str(), "agent_id": str(), "mission_id": str(), "actor_id": str(),
		"trace_id": str(), "payload": freeObject(), "refs": stringMap(),
	})
	journalList := object(map[string]any{
		"entries": array(journalEntry), "next_cursor": str(), "count": integer(),
	})
	spendAgent := object(map[string]any{"date": str(), "crew_id": str(), "agent_id": str(), "cost_usd": number(), "call_count": integer()})
	spendRoutine := object(map[string]any{"date": str(), "pipeline_id": str(), "pipeline_slug": str(), "cost_usd": number(), "run_count": integer()})
	topJournalSpender := object(map[string]any{"kind": str(), "id": str(), "label": str(), "cost_usd": number()})
	journalSpend := object(map[string]any{
		"window": str(), "total_cost_usd": number(), "by_agent": array(spendAgent), "by_routine": array(spendRoutine),
		"top_routines": array(topJournalSpender), "top_runs": array(topJournalSpender), "truncated": boolean(),
	})
	paymasterRow := func(key string) map[string]any {
		return object(map[string]any{key: str(), "cost_usd": number(), "call_count": integer(), "input_tokens": integer(), "output_tokens": integer()})
	}
	missionSpend := object(map[string]any{"mission_id": str(), "cost_usd": number(), "call_count": integer(), "input_tokens": integer(), "output_tokens": integer(), "first_ts": dateTime(), "last_ts": dateTime()})
	topPaymaster := object(map[string]any{"scope_kind": str(), "scope_id": str(), "cost_usd": number(), "call_count": integer()})
	subscription := object(map[string]any{"subscription_plan": str(), "provider": str(), "call_count": integer(), "input_tokens": integer(), "output_tokens": integer(), "last_ts": dateTime()})
	// Chain graph (GET /api/v1/chains/{anchor}). Flat node/edge lists rather
	// than a nested tree, because the underlying data is a graph: a run
	// reached from both its routine and its issue has two parents, and any
	// tree encoding would have to drop one of those edges.
	chainNode := object(map[string]any{
		"id": str(), "kind": str(), "ref": str(), "key": str(), "label": str(),
		"status": str(), "depth": integer(), "anchor": boolean(),
		"partial": boolean(), "partial_reason": str(),
	})
	chainEdge := object(map[string]any{"from": str(), "to": str(), "kind": str()})
	// gaps names the links the schema does not carry, so a client can tell
	// "nothing is attached" apart from "we cannot see what is attached".
	chainGap := object(map[string]any{"from": str(), "to": str(), "reason": str()})
	chainGraph := object(map[string]any{
		"anchor": str(), "anchor_node": str(), "max_depth": integer(), "max_nodes": integer(),
		"nodes": array(chainNode), "edges": array(chainEdge),
		"truncated": boolean(), "truncated_by": str(), "gaps": array(chainGap),
	})
	flag := object(map[string]any{
		"id": str(), "key": str(), "description": nullableString(), "enabled": boolean(), "percentage": integer(),
		"created_at": dateTime(), "updated_at": dateTime(), "override_enabled": map[string]any{"type": "boolean", "nullable": true},
	})
	channel := object(map[string]any{
		"id": str(), "workspace_id": str(), "type": str(), "url": str(), "to": str(), "events": array(str()), "enabled": boolean(),
		"created_by": str(), "created_at": dateTime(), "provider": str(), "scope": str(), "owner_user_id": str(),
		"categories": array(str()), "min_priority": str(),
	})
	providerField := object(map[string]any{"key": str(), "label": str(), "type": str(), "required": boolean(), "secret": boolean(), "placeholder": str(), "help": str(), "help_url": str()})
	provider := object(map[string]any{"provider": str(), "scheme": str(), "label": str(), "blurb": str(), "category": str(), "fields": array(providerField), "enabled": boolean()})
	delivery := object(map[string]any{
		"id": str(), "workspace_id": str(), "channel_id": str(), "user_id": str(), "category": str(), "dedup_key": str(),
		"source_kind": str(), "source_id": str(), "title": str(), "status": str(), "error": str(), "attempts": integer(),
		"created_at": dateTime(), "updated_at": dateTime(), "sent_at": dateTime(),
	})
	prefCell := object(map[string]any{"category": str(), "channel_id": str(), "state": map[string]any{"type": "string", "enum": []string{"off", "immediate"}}})
	timeseriesBucket := object(map[string]any{"ts": dateTime(), "series": map[string]any{"type": "object", "additionalProperties": number()}})
	timeseries := object(map[string]any{
		"metric":   map[string]any{"type": "string", "enum": []string{"issues_closed", "cost_usd", "runs_count", "active_missions"}},
		"window":   map[string]any{"type": "string", "enum": []string{"24h", "7d", "30d"}},
		"bucket":   map[string]any{"type": "string", "enum": []string{"15m", "1h", "1d"}},
		"group_by": map[string]any{"type": "string", "enum": []string{"crew", "model", "status", "none"}},
		"buckets":  array(timeseriesBucket), "series_labels": stringMap(),
	})
	rosterRow := object(map[string]any{"agent_id": str(), "crew_id": str(), "status": str(), "since": dateTime(), "details": freeObject()})
	missionMetrics := object(map[string]any{
		"total_missions": integer(), "active_missions": integer(), "completed_24h": integer(), "failed_24h": integer(),
		"total_tokens_24h": integer(), "total_cost_24h": number(), "avg_completion_time_ms": integer(),
		"tasks_completed_24h": integer(), "tasks_failed_24h": integer(),
	})
	journalLookup := object(map[string]any{
		"crews":    array(object(map[string]any{"id": str(), "name": str(), "slug": str(), "icon": nullableString(), "color": nullableString()})),
		"agents":   array(object(map[string]any{"id": str(), "name": str(), "slug": str(), "crew_id": nullableString(), "avatar_seed": nullableString(), "avatar_style": nullableString()})),
		"missions": array(object(map[string]any{"id": str(), "title": str(), "status": str()})),
	})
	runtime := object(map[string]any{
		"available": boolean(), "runtime": nullableString(), "version": nullableString(), "socket": nullableString(),
		"runtimes":      array(object(map[string]any{"runtime": str(), "version": str(), "socket": str(), "in_use": boolean()})),
		"install_links": stringMap(),
	})
	capacity := object(map[string]any{
		"enabled": boolean(), "limits": freeObject(), "in_flight_starts": integer(), "held": array(object(map[string]any{
			"crew_id": str(), "crew_slug": str(), "reason": str(), "detail": str(), "since": dateTime(), "waited_ms": integer(),
		})), "held_total": integer(), "host_signal_available": boolean(), "host_signal_error": str(), "host": freeObject(),
	})

	json := func(properties map[string]any) map[string]any { return object(properties) }
	ok := object(map[string]any{"ok": boolean()})
	return map[string]DomainSchema{
		"GET /api/v1/journal":                                {Response: journalList},
		"GET /api/v1/journal/stream":                         {Response: journalEntry, ResponseMedia: []string{"text/event-stream"}},
		"GET /api/v1/journal/count":                          {Response: object(map[string]any{"total": integer()})},
		"GET /api/v1/journal/{id}":                           {Response: journalEntry},
		"GET /api/v1/journal/lookup":                         {Response: journalLookup},
		"GET /api/v1/journal/spend":                          {Response: journalSpend},
		"POST /api/v1/journal/{id}/priority":                 {Request: json(map[string]any{"priority": str(), "reason": str()}), Response: ok},
		"GET /api/v1/paymaster/spend/by-crew":                {Response: object(map[string]any{"rows": array(paymasterRow("crew_id")), "since": dateTime(), "until": dateTime()})},
		"GET /api/v1/paymaster/spend/by-agent/{crewId}":      {Response: object(map[string]any{"rows": array(paymasterRow("agent_id")), "crew_id": str()})},
		"GET /api/v1/paymaster/spend/by-mission/{missionId}": {Response: object(map[string]any{"row": missionSpend, "mission_id": str()})},
		"GET /api/v1/paymaster/top-spenders":                 {Response: object(map[string]any{"rows": array(topPaymaster), "limit": integer(), "since": dateTime()})},
		"GET /api/v1/paymaster/subscriptions":                {Response: object(map[string]any{"rows": array(subscription), "since": dateTime(), "until": dateTime()})},
		"GET /api/v1/chains/{anchor}":                        {Response: chainGraph},
		"GET /api/v1/metrics/timeseries":                     {Response: timeseries},
		"GET /api/v1/mission-metrics":                        {Response: missionMetrics},
		"GET /api/v1/feature-flags":                          {Response: array(flag)},
		"POST /api/v1/feature-flags":                         {Request: json(map[string]any{"key": str(), "description": nullableString(), "enabled": boolean(), "percentage": integer()}), Response: flag},
		"PATCH /api/v1/feature-flags/{key}":                  {Request: json(map[string]any{"description": nullableString(), "enabled": boolean(), "percentage": integer()}), Response: flag},
		"PUT /api/v1/feature-flags/{key}/override":           {Request: json(map[string]any{"enabled": boolean()}), Response: object(map[string]any{"key": str(), "enabled": boolean()})},
		"GET /api/v1/presence/roster":                        {Response: object(map[string]any{"rows": array(rosterRow), "count": integer()})},
		"GET /api/v1/system/runtime":                         {Response: runtime},
		"GET /api/v1/runtime/capacity":                       {Response: capacity},
		"GET /api/v1/notification-channels":                  {Response: object(map[string]any{"channels": array(channel)})},
		"POST /api/v1/notification-channels":                 {Request: json(map[string]any{"type": str(), "url": str(), "to": str(), "secret": str(), "events": array(str()), "provider": str(), "fields": stringMap(), "shoutrrr_url": str(), "personal": boolean(), "categories": array(str()), "min_priority": str()}), Response: object(map[string]any{"id": str(), "workspace_id": str(), "type": str(), "url": str(), "to": str(), "events": array(str()), "enabled": boolean(), "created_by": str(), "created_at": dateTime(), "provider": str(), "scope": str(), "owner_user_id": str(), "categories": array(str()), "min_priority": str(), "secret": str()})},
		"PATCH /api/v1/notification-channels/{id}":           {Request: json(map[string]any{"enabled": boolean(), "categories": array(str()), "min_priority": str(), "events": array(str())}), Response: object(map[string]any{"updated": str()})},
		"DELETE /api/v1/notification-channels/{id}":          {Response: object(map[string]any{"deleted": str()})},
		"GET /api/v1/notification-providers":                 {Response: object(map[string]any{"providers": array(provider), "categories": array(freeObject())})},
		"PATCH /api/v1/notification-providers/{provider}":    {Request: json(map[string]any{"enabled": boolean()}), Response: object(map[string]any{"provider": str(), "enabled": boolean()})},
		"GET /api/v1/notification-templates":                 {Response: object(map[string]any{"templates": array(object(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()}))})},
		"PUT /api/v1/notification-templates":                 {Request: json(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()}), Response: object(map[string]any{"category": str(), "channel_id": str(), "title": str(), "body": str()})},
		"GET /api/v1/notification-deliveries":                {Response: object(map[string]any{"deliveries": array(delivery)})},
		"GET /api/v1/me/notification-prefs":                  {Response: object(map[string]any{"cells": array(prefCell)})},
		"PUT /api/v1/me/notification-prefs":                  {Request: json(map[string]any{"cells": array(prefCell)}), Response: ok},
		"POST /api/v1/notification-channels/test":            {Request: json(map[string]any{"type": str(), "provider": str(), "fields": stringMap(), "shoutrrr_url": str(), "url": str(), "secret": str(), "to": str()}), Response: ok},
		"POST /api/v1/notification-channels/{id}/test":       {Response: object(map[string]any{"ok": boolean(), "channel_id": str()})},
	}
}
