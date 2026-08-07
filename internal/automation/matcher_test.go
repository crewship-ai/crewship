package automation

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestMatcherFields(t *testing.T) {
	base := journal.Entry{
		WorkspaceID: "ws_1",
		CrewID:      "c_1",
		AgentID:     "a_1",
		MissionID:   "m_1",
		Severity:    journal.SeverityWarn,
		// `action` + `details` are the real mission.status_change payload
		// (internal/api/issue_events.go). `count` / `flag` are here for the
		// non-string comparison arms, not because any emitter writes them.
		Payload: map[string]any{"action": "status_changed", "details": "TODO → DONE", "count": 3, "flag": true},
	}

	cases := []struct {
		name string
		m    Matcher
		want bool
	}{
		{"empty matches everything", Matcher{}, true},
		{"crew hit", Matcher{CrewIDs: []string{"c_0", "c_1"}}, true},
		{"crew miss", Matcher{CrewIDs: []string{"c_9"}}, false},
		{"agent hit", Matcher{AgentIDs: []string{"a_1"}}, true},
		{"agent miss", Matcher{AgentIDs: []string{"a_9"}}, false},
		{"mission hit", Matcher{MissionIDs: []string{"m_1"}}, true},
		{"mission miss", Matcher{MissionIDs: []string{"m_9"}}, false},
		{"severity hit", Matcher{Severities: []string{"warn", "error"}}, true},
		{"severity miss", Matcher{Severities: []string{"error"}}, false},
		{"payload string hit", Matcher{PayloadEquals: map[string]any{"action": "status_changed"}}, true},
		{"payload string miss", Matcher{PayloadEquals: map[string]any{"action": "created"}}, false},
		// The key the docs used to name. It has never existed on any entry;
		// a rule built on it is saved, listed, and never fires.
		{"payload key that no emitter writes", Matcher{PayloadEquals: map[string]any{"to": "DONE"}}, false},
		{"payload bool hit", Matcher{PayloadEquals: map[string]any{"flag": true}}, true},
		{"payload absent key", Matcher{PayloadEquals: map[string]any{"nope": "x"}}, false},
		// Every populated field must be satisfied — the fields are ANDed,
		// like hooks.Matcher's.
		{"fields are ANDed", Matcher{CrewIDs: []string{"c_1"}, AgentIDs: []string{"a_9"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Matches(base); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// A matcher stored as JSON has been through SQLite as text, so its numbers
// come back as float64 while the live payload's are ints. A rule that matched
// on the day it was written must still match after a restart.
func TestMatcherPayloadEqualsSurvivesAJSONRoundTrip(t *testing.T) {
	stored, err := json.Marshal(Matcher{PayloadEquals: map[string]any{"count": 3}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded Matcher
	if err := json.Unmarshal(stored, &reloaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := journal.Entry{Payload: map[string]any{"count": 3}}
	if !reloaded.Matches(e) {
		t.Error("a reloaded matcher stopped matching a payload it matched before the round trip")
	}
}
