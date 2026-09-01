package automation

import "github.com/crewship-ai/crewship/internal/journal"

// KnownPayloadKeys is a hand-curated, DELIBERATELY INCOMPLETE map from
// journal entry type to the payload keys its emitter is known to write. It
// exists so `matcher.payload_equals` can be checked for the one mistake
// PayloadEquals's own doc comment names: "a key NO emitter writes is not an
// error here and cannot be — this type knows nothing about the 117 journal
// entry types or their payloads."
//
// # Why this is not generated, unlike the event-type registry
//
// journal.AllEntryTypes (internal/journal/registry_generated.go) can be
// generated because EVERY entry type is declared exactly once, in one place,
// as a typed string constant — a values problem with one honest answer.
// Payload keys are not: they are assembled across ~130 call sites, many
// through a helper function several frames from the journal.Entry{} literal
// (e.g. internal/api/issue_events.go's issueEventPayload, which is what
// mission.status_change/mission.created/mission.assigned/mission.comment
// actually share), some conditionally per branch, none through a declared
// schema type. A static scan that tried to recover this would either miss
// most of it (an AST walk sees the literal `Payload: map[string]any{...}` at
// the Emit call site, not the value returned by a function three files away)
// or, worse, guess wrong and reject a key that IS valid — which is a worse
// failure than the silent one this feature replaces, because it would look
// like the fix is working while actively refusing correct rules.
//
// So this is a plain Go map, populated by reading the real emitter for each
// entry below (call sites cited in each comment) — the same standard
// docs/guides/automations.mdx already asks a rule author to meet by hand
// ("read one real entry before writing the predicate"). It covers the event
// types most likely to be matched on today, not all ~129. An event type
// absent from this map gets NO payload-key validation — see ValidPayloadKey.
//
// Extend this when you add automation support for (or personally rely on) a
// new event type; do not delete an entry to make a rule pass, and do not add
// a key you have not confirmed against the emitter — a wrong entry here is
// exactly the false-rejection risk the generated approach was rejected for.
var KnownPayloadKeys = map[journal.EntryType][]string{
	// mission.status_change / mission.created / mission.assigned /
	// mission.comment all go through issueEventPayload
	// (internal/api/issue_events.go:157-169), which always writes `action`
	// and `details`, and additionally `from`/`to` ONLY when the event is a
	// status transition (journalTypeForIssueAction routes actionStatusChanged
	// to EntryMissionStatus, so `from`/`to` in practice only ever appear on
	// that type). Pinned against the real emitter by
	// api.TestIssueEvents_JournalPayloadIsWhatAutomationsMatchOn.
	journal.EntryMissionStatus:   {"action", "details", "from", "to"},
	journal.EntryMissionCreated:  {"action", "details"},
	journal.EntryMissionAssigned: {"action", "details"},
	journal.EntryMissionComment:  {"action", "details"},

	// automation.throttled — internal/automation/registry.go's throttleEntry.
	journal.EntryAutomationThrottled: {
		"automation_id", "automation_name", "event_type", "max_per_hour", "window_started_at",
	},

	// automation.depth_exceeded — two emit sites in
	// internal/automation/registry.go (emitChainUnreadable,
	// emitDepthExceeded); the set below is the union of both, since either
	// shape is a legitimate entry of this type.
	journal.EntryAutomationDepthExceeded: {
		"automation_id", "automation_name", "routine_slug", "origin_run_id",
		"reason", "error", "chain_depth", "max_chain_depth",
	},

	// pipeline.schedule.circuit_breaker_tripped —
	// internal/pipeline/schedules.go's disableForCircuitBreaker.
	journal.EntryPipelineScheduleCircuitBreaker: {
		"schedule_id", "pipeline_slug", "consecutive_failures", "max_consecutive_failures",
	},

	// pipeline.schedule.missed_occurrences — internal/pipeline/schedules.go,
	// the catch-up emit around fireOne.
	journal.EntryPipelineScheduleMissedOccurrences: {
		"schedule_id", "missed_count", "window_start", "window_end",
	},

	// page.wake.fired — internal/api/pages_wake_issue.go's wake-gate emit.
	journal.EntryPageWakeFired: {
		"page", "page_id", "panel", "gate", "crew", "writes",
		"issue_id", "issue_identifier", "automation_id", "coalesced_events",
	},
}

// ValidPayloadKey reports whether key is a documented payload field of
// eventType, per KnownPayloadKeys.
//
// Absence of eventType from KnownPayloadKeys returns true (no opinion), not
// false: the map is a curated subset, not an exhaustive contract, and
// rejecting every key for a type nobody has catalogued yet would turn an
// incomplete registry into a feature that stops working for 120 of ~129
// event types. See the package doc on KnownPayloadKeys for why a wrong
// "no" (missing a real key) is a worse failure than a wrong "yes" (a typo
// against an uncatalogued type slipping through) — the latter is exactly
// today's behaviour and is called out honestly in the docs and the save
// error, not silently accepted as fine.
func ValidPayloadKey(eventType journal.EntryType, key string) bool {
	keys, ok := KnownPayloadKeys[eventType]
	if !ok {
		return true
	}
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}
