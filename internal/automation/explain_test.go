package automation

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// A matcher that never fires is the worst thing this feature can do, because
// nothing tells you. `crewship automation create` warns about it in its own
// help text, which is an admission rather than a fix: the documented first
// example shipped with `--payload-equals to=DONE` against an event whose
// payload has no `to` key, so the very first rule a reader builds is a rule
// that silently does nothing.
//
// Matches answers yes/no. Explain answers WHY not — which clause rejected,
// what it wanted, and what was actually there. That difference is what turns
// a silent rule into a debuggable one.

func entryFor(payload map[string]any) journal.Entry {
	return journal.Entry{
		WorkspaceID: "ws_1",
		Type:        journal.EntryMissionStatus,
		CrewID:      "crew_1",
		AgentID:     "agent_1",
		MissionID:   "m_1",
		Severity:    journal.SeverityInfo,
		Payload:     payload,
	}
}

func TestExplain_MatchIsReportedAsAMatch(t *testing.T) {
	m := Matcher{CrewIDs: []string{"crew_1"}}
	r := m.Explain(entryFor(map[string]any{"action": "status_changed"}))
	if !r.Matched {
		t.Fatalf("want match, got rejection by %q", r.Clause)
	}
	if r.Clause != "" {
		t.Errorf("a match must name no clause, got %q", r.Clause)
	}
}

func TestExplain_NamesTheClauseThatRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		m      Matcher
		clause string
	}{
		{"crew", Matcher{CrewIDs: []string{"other"}}, "crew_ids"},
		{"agent", Matcher{AgentIDs: []string{"other"}}, "agent_ids"},
		{"mission", Matcher{MissionIDs: []string{"other"}}, "mission_ids"},
		{"severity", Matcher{Severities: []string{"error"}}, "severities"},
		{"payload", Matcher{PayloadEquals: map[string]any{"action": "nope"}}, "payload_equals.action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.m.Explain(entryFor(map[string]any{"action": "status_changed"}))
			if r.Matched {
				t.Fatal("want rejection")
			}
			if r.Clause != tc.clause {
				t.Errorf("Clause = %q, want %q", r.Clause, tc.clause)
			}
		})
	}
}

// The single most useful diagnostic: a predicate on a key the event does not
// carry. "to != DONE" reads like a value mismatch you could fix by changing
// DONE; "there is no key `to`" tells you the rule can never match anything.
func TestExplain_SaysWhenThePayloadKeyIsAbsentEntirely(t *testing.T) {
	m := Matcher{PayloadEquals: map[string]any{"to": "DONE"}}
	r := m.Explain(entryFor(map[string]any{"action": "status_changed", "details": "BACKLOG → TODO"}))

	if r.Matched {
		t.Fatal("want rejection")
	}
	if !r.KeyAbsent {
		t.Error("KeyAbsent must be set: a missing key can never match, whatever the value is changed to")
	}
	// The keys that ARE there are the fix, so they belong in the answer.
	if !strings.Contains(r.Detail, "action") || !strings.Contains(r.Detail, "details") {
		t.Errorf("Detail must list the keys the entry does carry, got %q", r.Detail)
	}
}

func TestExplain_ValueMismatchIsNotReportedAsAMissingKey(t *testing.T) {
	m := Matcher{PayloadEquals: map[string]any{"action": "nope"}}
	r := m.Explain(entryFor(map[string]any{"action": "status_changed"}))
	if r.KeyAbsent {
		t.Error("the key is present; reporting it absent sends the reader to fix the wrong thing")
	}
	if !strings.Contains(r.Detail, "status_changed") {
		t.Errorf("Detail must show the value that was actually there, got %q", r.Detail)
	}
}

// Explain must agree with Matches on every input, or the diagnostic describes
// a matcher that is not the one doing the work.
func TestExplain_AgreesWithMatches(t *testing.T) {
	entries := []journal.Entry{
		entryFor(map[string]any{"action": "status_changed"}),
		entryFor(nil),
		{WorkspaceID: "ws_1", Type: journal.EntryRunFailed, Severity: journal.SeverityError},
	}
	matchers := []Matcher{
		{},
		{CrewIDs: []string{"crew_1"}},
		{CrewIDs: []string{"other"}},
		{Severities: []string{"error"}},
		{PayloadEquals: map[string]any{"action": "status_changed"}},
		{PayloadEquals: map[string]any{"to": "DONE"}},
		{AgentIDs: []string{"agent_1"}, MissionIDs: []string{"m_1"}},
	}
	for i, m := range matchers {
		for j, e := range entries {
			if got, want := m.Explain(e).Matched, m.Matches(e); got != want {
				t.Errorf("matcher %d / entry %d: Explain says %v, Matches says %v", i, j, got, want)
			}
		}
	}
}
