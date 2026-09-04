package automation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// KnownPayloadKeys is a hand-curated, DELIBERATELY INCOMPLETE map from
// journal entry type to the payload keys its emitter is known to write. It
// exists so `matcher.payload_equals` can be checked for the one mistake
// PayloadEquals's own doc comment names: "a key NO emitter writes is not an
// error here and cannot be — this type knows nothing about the journal's
// registered entry types or their payloads."
//
// # Why this is not generated, unlike the event-type registry
//
// journal.AllEntryTypes (internal/journal/registry_generated.go) can be
// generated because EVERY entry type is declared exactly once, in one place,
// as a typed string constant — a values problem with one honest answer.
// Payload keys are not: they are assembled across every emitter call site, many
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
// types most likely to be matched on today, not the full registry. An event type
// absent from this map gets NO payload-key validation — see ValidPayloadKey.
//
// Extend this when you add automation support for (or personally rely on) a
// new event type; do not delete an entry to make a rule pass, and do not add
// a key you have not confirmed against the emitter — a wrong entry here is
// exactly the false-rejection risk the generated approach was rejected for.
var KnownPayloadKeys = map[journal.EntryType][]string{
	// mission.status_change / mission.created / mission.assigned all go
	// through issueEventPayload (internal/api/issue_events.go:157-169), which
	// always writes `action` and `details`, and additionally `from`/`to`
	// ONLY when the event is a status transition (journalTypeForIssueAction
	// routes actionStatusChanged to EntryMissionStatus, so `from`/`to` in
	// practice only ever appear on that type). Pinned against the real
	// emitter by api.TestIssueEvents_JournalPayloadIsWhatAutomationsMatchOn.
	//
	// mission.comment is deliberately absent from this map, even though it
	// is a real, frequently-matched type. It has TWO emitters with entirely
	// disjoint payload shapes: issueEventPayload's {action, details} (an
	// issue-comment event) and internal/api/assignments_run.go's mission
	// comment mirror, which writes {comment_id, mission_id, author_name,
	// target_name, target_slug, body} and never writes action or details.
	// Neither the union nor either shape alone is a correct key set for
	// "mission.comment" as a type: the union would accept a payload_equals
	// rule on `action` that can never match an assignments_run.go-sourced
	// entry, and only the second emitter's keys can be found in a payload
	// that came from the first. A wrong "no" on a real key — rejecting a
	// valid predicate — is the failure this map's own doc comment calls
	// worse than the silent one, so this type gets no key validation (see
	// ValidPayloadKey) rather than a set that is wrong for half its
	// instances.
	journal.EntryMissionStatus:   {"action", "details", "from", "to"},
	journal.EntryMissionCreated:  {"action", "details"},
	journal.EntryMissionAssigned: {"action", "details"},

	// automation.throttled — internal/automation/registry.go's throttleEntry.
	journal.EntryAutomationThrottled: {
		"automation_id", "automation_name", "event_type", "max_per_hour", "window_started_at",
	},

	// automation.depth_exceeded — THREE emit sites: internal/automation/
	// registry.go's emitChainUnreadable and emitDepthExceeded, plus
	// internal/pipeline/journal.go's (*pipelineEmitContext).emitDepthExceeded
	// (the composition-chain depth cap, distinct from the automation-chain
	// one despite sharing an entry type). The set below is the union of all
	// three, since any of the three shapes is a legitimate entry of this
	// type; the pipeline emitter additionally writes chain_origin, edge,
	// pipeline_id, pipeline_slug and run_id, none of which the
	// internal/automation emitters write.
	journal.EntryAutomationDepthExceeded: {
		"automation_id", "automation_name", "routine_slug", "origin_run_id",
		"reason", "error", "chain_depth", "max_chain_depth",
		"chain_origin", "edge", "pipeline_id", "pipeline_slug", "run_id",
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
// incomplete registry into a feature that stops working for most of the
// full registry's event types. See the package doc on KnownPayloadKeys for why a wrong
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

// maxEventTypeSuggestions bounds how many alternatives an unregistered-type
// error names. journal.AllEntryTypes is the full registry; naming all of it
// in a 400 body would bury the useful part of the message.
const maxEventTypeSuggestions = 8

// eventTypeHint builds the "name the valid alternatives" half of
// Validate's unregistered-event_type error. It prefers types sharing the
// same namespace as the rejected value (a typo of "mission.status_change"
// should surface other mission.* types, not a random sample of the whole
// registry) and falls back to the first few types alphabetically when
// nothing shares a namespace — plus, either way, a pointer to confirming a
// specific type against real history.
//
// Moved here (from internal/api/automations.go) in the same change that
// moved the registry check itself into Validate — see Validate's doc
// comment for why.
func eventTypeHint(rejected string) string {
	namespace := rejected
	if i := strings.IndexByte(rejected, '.'); i >= 0 {
		namespace = rejected[:i]
	}
	var suggestions []string
	for _, t := range journal.AllEntryTypes {
		if strings.HasPrefix(string(t), namespace+".") {
			suggestions = append(suggestions, string(t))
		}
	}
	sort.Strings(suggestions)
	how := fmt.Sprintf("run `crewship journal --type <t> --lines 1` to confirm one exists, "+
		"or see docs/guides/automations.mdx for the full closed list of %d types", len(journal.AllEntryTypes))
	if len(suggestions) == 0 {
		suggestions = make([]string, 0, maxEventTypeSuggestions)
		for _, t := range journal.AllEntryTypes {
			suggestions = append(suggestions, string(t))
			if len(suggestions) == maxEventTypeSuggestions {
				break
			}
		}
		return fmt.Sprintf("no registered type starts with %q — a few examples: %s (%s)",
			namespace, strings.Join(suggestions, ", "), how)
	}
	truncated := len(suggestions) > maxEventTypeSuggestions
	if truncated {
		suggestions = suggestions[:maxEventTypeSuggestions]
	}
	suffix := ""
	if truncated {
		suffix = ", …"
	}
	return fmt.Sprintf("registered types starting with %q: %s%s (%s)",
		namespace+".", strings.Join(suggestions, ", "), suffix, how)
}

// validatePayloadEqualsKeys rejects any matcher.payload_equals key that is
// not a documented payload field of eventType, per KnownPayloadKeys.
//
// KnownPayloadKeys is a curated subset, not every registered event type (see
// its doc comment for why an exhaustive, generated version was rejected) —
// ValidPayloadKey returns true with no opinion for an eventType it does not
// catalogue, so this only ever rejects a key against a type this package has
// actually verified the emitter for.
func validatePayloadEqualsKeys(eventType journal.EntryType, payloadEquals map[string]any) error {
	if len(payloadEquals) == 0 {
		return nil
	}
	keys := make([]string, 0, len(payloadEquals))
	for k := range payloadEquals {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic: report the same offending key every time, not a random map order
	for _, k := range keys {
		if ValidPayloadKey(eventType, k) {
			continue
		}
		known, ok := KnownPayloadKeys[eventType]
		if !ok {
			// Unreachable in practice — ValidPayloadKey returns true for an
			// uncatalogued type — but keep the branch honest rather than
			// panic-indexing a nil slice if that contract ever changes.
			return fmt.Errorf("automation: matcher.payload_equals key %q is not a documented payload field of %q", k, eventType)
		}
		return fmt.Errorf("automation: matcher.payload_equals key %q is not a documented payload field of %q; "+
			"known keys: %s — read one real entry first with `crewship journal --type %s --lines 1 --format json`",
			k, eventType, strings.Join(known, ", "), eventType)
	}
	return nil
}
