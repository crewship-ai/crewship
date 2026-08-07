// Package automation turns a journal event into a deferred routine run.
//
// The whole path is four hops, and three of them already existed:
//
//	journal commit → AddCommitObserver → match automations → pending_runs
//	   (journal)        (journal)          (THIS PACKAGE)     (pipeline)
//
// What this package adds is the middle: an in-memory Registry of the rules a
// workspace has stored, a matcher that evaluates one against a journal.Entry,
// and an Observer that can be handed to journal.Writer.AddCommitObserver.
//
// # The constraint that shapes everything here
//
// A commit observer runs ON THE JOURNAL WRITE PATH (see internal/journal/
// emit.go:79-86). It must be cheap, must not block, and must consume its
// slice synchronously. So:
//
//   - Matching never touches the database. The Registry holds the rules in
//     memory and reloads them on write and on a 60s tick; Observer reads a
//     map and nothing else.
//   - Observer never calls Enqueue. It coalesces matches into intents keyed
//     by debounce key and returns; a background flusher performs the INSERT.
//     A status-change storm of 200 entries therefore produces ONE enqueue,
//     not 200 — the in-memory coalescing does that, and pending_runs' own
//     (pipeline_id, debounce_key) coalescing catches whatever crosses a
//     flush boundary.
//   - Observer copies every value it needs out of the entry before returning.
//     The backing array is reused the moment it does.
//
// An automation can only ENQUEUE. It never executes a routine, never writes
// to an issue, and holds no veto over anything — which is exactly what makes
// it safe to run here, and why it is not a hooks_config row (that layer is
// blocking and crew-scoped, and holds a veto).
package automation

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ActionKindRoutine is the only action kind v1 accepts. The column exists so
// 'issue' / 'notify' can land without a migration; anything else is rejected
// at write time rather than discovered at fire time by a rule that silently
// does nothing.
const ActionKindRoutine = "routine"

// ErrNotFound is returned when an automation id does not resolve inside the
// caller's workspace (or was soft-deleted). A row in ANOTHER workspace is
// reported as not-found, never as forbidden — the caller has no business
// learning that the id exists.
var ErrNotFound = errors.New("automation: not found")

// Matcher is the predicate a stored rule carries. It has the same SHAPE as
// hooks.Matcher (crew_ids / agent_ids / severities, plus mission_ids and
// payload_equals) so the two can converge later if we decide they should.
//
// Semantics match hooks': every POPULATED field must be satisfied; an empty
// field is "don't care". The zero-value Matcher therefore matches every entry
// of the automation's event type, which is the documented meaning of
// `matcher_json = '{}'`.
//
// Unlike hooks.Matcher there is no Tools field and no regex anywhere: a
// journal entry has no tool name, and a regex on the write path is a
// pathological-backtracking incident waiting for the first user who pastes
// one in. Exact match only.
type Matcher struct {
	CrewIDs    []string `json:"crew_ids,omitempty"`
	AgentIDs   []string `json:"agent_ids,omitempty"`
	MissionIDs []string `json:"mission_ids,omitempty"`
	Severities []string `json:"severities,omitempty"`
	// PayloadEquals requires entry.Payload[k] to equal v for every pair.
	// Comparison is by JSON encoding, so a rule stored as {"count": 3}
	// still matches a payload that arrived with a numeric or boolean value
	// of the same shape — the alternative is a rule that matches in tests
	// and not in production because one side round-tripped through SQLite.
	//
	// A key NO emitter writes is not an error here and cannot be: this type
	// knows nothing about the 117 journal entry types or their payloads. The
	// rule is simply saved and matches nothing, which is the silent failure
	// `--payload-equals to=DONE` shipped as the documented first example for
	// months. Keys per entry type are documented in docs/guides/automations.mdx
	// and pinned against the real emitter by
	// api.TestIssueEvents_JournalPayloadIsWhatAutomationsMatchOn.
	PayloadEquals map[string]any `json:"payload_equals,omitempty"`
}

// Matches evaluates m against e. Pure and allocation-free on the common path
// (an empty matcher), because it runs once per (entry × automation) on the
// journal write path.
func (m Matcher) Matches(e journal.Entry) bool {
	if len(m.CrewIDs) > 0 && !contains(m.CrewIDs, e.CrewID) {
		return false
	}
	if len(m.AgentIDs) > 0 && !contains(m.AgentIDs, e.AgentID) {
		return false
	}
	if len(m.MissionIDs) > 0 && !contains(m.MissionIDs, e.MissionID) {
		return false
	}
	if len(m.Severities) > 0 && !contains(m.Severities, string(e.Severity)) {
		return false
	}
	for k, want := range m.PayloadEquals {
		got, ok := e.Payload[k]
		if !ok || !jsonEqual(got, want) {
			return false
		}
	}
	return true
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// jsonEqual compares two payload values by their JSON encoding. A stored
// matcher has been through SQLite as text, so its 3 is a float64 while the
// live payload's 3 may be an int; comparing the encodings makes the rule mean
// the same thing on both sides of a restart.
func jsonEqual(a, b any) bool {
	ab, aerr := json.Marshal(a)
	bb, berr := json.Marshal(b)
	if aerr != nil || berr != nil {
		return false
	}
	return string(ab) == string(bb)
}

// Action is the decoded action_config_json for ActionKindRoutine.
//
// Inputs values may reference the triggering entry with {{ event.mission_id }},
// {{ event.agent_id }}, {{ event.crew_id }}, {{ event.run_id }} and
// {{ event.payload.<key> }} — rendered with pipeline.Render, the SAME renderer
// routine steps use. There is deliberately no second templating language here.
type Action struct {
	RoutineSlug string         `json:"routine_slug"`
	Inputs      map[string]any `json:"inputs,omitempty"`
}

// Automation is one stored rule.
type Automation struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	// EventType is a journal.EntryType. Exactly one per row: an automation
	// that fires on "anything" is a support ticket waiting to happen.
	EventType  string  `json:"event_type"`
	Matcher    Matcher `json:"matcher"`
	ActionKind string  `json:"action_kind"`
	Action     Action  `json:"action"`
	// DebounceSeconds is how long pending_runs holds the enqueued run open
	// for further events to coalesce into.
	DebounceSeconds int `json:"debounce_seconds"`
	// MaxPerHour caps how many RUNS this automation may cause per rolling
	// hour window. Over the cap the match is dropped and a single
	// automation.throttled entry is written for the window.
	MaxPerHour int        `json:"max_per_hour"`
	CreatedBy  string     `json:"created_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

// Default burst controls, applied when a caller leaves them unset. They match
// the column defaults in the migration; both places state them because a row
// inserted by the store never relies on the DB default (it writes every
// column) and a value that differs between the two would be a silent
// behaviour change on restore-from-backup.
const (
	DefaultDebounceSeconds = 10
	DefaultMaxPerHour      = 60

	// maxDebounceSeconds bounds how long a single burst may defer its run.
	// An hour is already well past "debounce" and into "schedule", which is
	// a different feature with its own table.
	maxDebounceSeconds = 3600
	// maxPerHourCeiling bounds the cap itself. A rule permitted 100k runs an
	// hour is not rate limited, it is a denial-of-service with a config file.
	maxPerHourCeiling = 10000
)

// Validate checks the fields a caller controls, defaulting the burst controls
// when unset. It is the single gate both the HTTP handler and the store use,
// so an automation created through any door has the same guarantees.
func (a *Automation) Validate() error {
	if a.WorkspaceID == "" {
		return errors.New("automation: workspace_id required")
	}
	if a.Name == "" {
		return errors.New("automation: name required")
	}
	if a.EventType == "" {
		return errors.New("automation: event_type required")
	}
	if a.ActionKind == "" {
		a.ActionKind = ActionKindRoutine
	}
	if a.ActionKind != ActionKindRoutine {
		return fmt.Errorf("automation: action_kind %q is not supported (only %q)", a.ActionKind, ActionKindRoutine)
	}
	if a.Action.RoutineSlug == "" {
		return errors.New("automation: action.routine_slug required")
	}
	if a.DebounceSeconds == 0 {
		a.DebounceSeconds = DefaultDebounceSeconds
	}
	if a.DebounceSeconds < 0 || a.DebounceSeconds > maxDebounceSeconds {
		return fmt.Errorf("automation: debounce_seconds must be between 0 and %d", maxDebounceSeconds)
	}
	if a.MaxPerHour == 0 {
		a.MaxPerHour = DefaultMaxPerHour
	}
	if a.MaxPerHour < 1 || a.MaxPerHour > maxPerHourCeiling {
		return fmt.Errorf("automation: max_per_hour must be between 1 and %d", maxPerHourCeiling)
	}
	return nil
}

// Resolved pairs a stored rule with the routine it targets. The Registry
// resolves routine_slug → pipeline id ONCE per refresh, off the write path,
// so Observer never has to look one up.
type Resolved struct {
	Automation
	PipelineID   string
	PipelineSlug string
}

// EventContext projects a journal entry into the {{ event.* }} render
// namespace. Exported because it is the contract an automation author writes
// against; the field names here are the ones that appear in docs.
func EventContext(e journal.Entry) map[string]any {
	return map[string]any{
		"mission_id": e.MissionID,
		"agent_id":   e.AgentID,
		"crew_id":    e.CrewID,
		// A journal entry's trace_id IS the originating run id — see
		// journal.prepareEntry, where WithRunID wins over OTel trace
		// context precisely so run-scoped entries share trace_id == run.id.
		"run_id":  e.TraceID,
		"payload": e.Payload,
	}
}
