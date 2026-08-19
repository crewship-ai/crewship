package memory

import (
	"strings"
	"testing"
)

// PathProjection is the API's honesty signal: it answers "could a row
// for this path ever exist", which is what separates an empty history
// from an unreadable one. It must agree with the audit watcher's own
// gate — both go through classifyMemoryFile — and it must name the
// owner of a file it cannot show, because "not here" without "it lives
// there" is how the panel got read as "the agent knows nothing".
func TestPathProjection(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		wantState ProjectionState
	}{
		{"agent canonical file", "agent:martin/AGENT.md", ProjectionRecorded},
		{"agent daily log", "agent:martin/daily/2026-08-13.md", ProjectionRecorded},
		{"agent pins", "agent:martin/pins.md", ProjectionRecorded},
		{"crew shared memory", "crew:crew_a/CREW.md", ProjectionRecorded},
		{"crew pins", "crew:crew_a/pins.md", ProjectionRecorded},
		{"crew learned topic", "crew:crew_a/learned-2026-08-13.md", ProjectionRecorded},
		{"crew daily log", "crew:crew_a/daily/2026-08-13.md", ProjectionRecorded},

		{"agent lessons has no writer into the trail", "agent:martin/lessons.md", ProjectionUnrecorded},
		{"crew lessons likewise", "crew:crew_a/lessons.md", ProjectionUnrecorded},
		{"persona has its own history endpoint", "agent:martin/PERSONA.md", ProjectionUnrecorded},
		{"peer cards have their own endpoint", "agent:martin/peers/u_ab12.md", ProjectionUnrecorded},
		{"staging proposals are not canonical", "crew:crew_a/.proposed/learned-x.md", ProjectionUnrecorded},
		{"quarantined content is never recorded", "agent:martin/.quarantine/abc.md", ProjectionUnrecorded},
		{"unknown scratch file", "agent:martin/notes.md", ProjectionUnrecorded},
		{"AGENT.md is not a shared-tier file", "crew:crew_a/AGENT.md", ProjectionUnrecorded},
		{"no scope prefix", "AGENT.md", ProjectionUnrecorded},
		{"scope prefix with no file", "agent:martin/", ProjectionUnrecorded},
		{"empty", "", ProjectionUnrecorded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PathProjection(tc.path)
			if got.State != tc.wantState {
				t.Fatalf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.Reason == "" {
				t.Error("reason is empty — every verdict has to be explainable to a reader")
			}
			if got.Readable() != (tc.wantState == ProjectionRecorded) {
				t.Errorf("Readable() = %v, inconsistent with state %q", got.Readable(), got.State)
			}
		})
	}
}

// The reason text is load-bearing: a reader told only "not available"
// goes looking for a bug. Told where the file actually is, they go
// read it.
func TestPathProjection_NamesTheOwnerOfWhatItCannotShow(t *testing.T) {
	if got := PathProjection("agent:martin/lessons.md"); !strings.Contains(got.Reason, "WriteLesson") {
		t.Errorf("lessons reason does not name its writer: %q", got.Reason)
	}
	if got := PathProjection("agent:martin/PERSONA.md"); !strings.Contains(got.Reason, "persona/history") {
		t.Errorf("persona reason does not point at its own endpoint: %q", got.Reason)
	}
	if got := PathProjection("agent:martin/peers/u_ab12.md"); !strings.Contains(got.Reason, "/peers") {
		t.Errorf("peers reason does not point at its own endpoint: %q", got.Reason)
	}
}

// classifyMemoryFile is the one gate the watcher and the API share.
// Its rejections are as load-bearing as its acceptances: recording
// .proposed/, .quarantine/ or .snapshots/ would put non-canonical
// bytes into the trail under a canonical name.
func TestClassifyMemoryFile_RefusesNonCanonicalSubtrees(t *testing.T) {
	for _, rel := range []string{
		".proposed/learned-2026-08-13.md",
		".quarantine/deadbeef.md",
		".snapshots/AGENT.md",
		"topics/.proposed/pins.md",
		"",
		".",
	} {
		if tier, ok := classifyMemoryFile(scopeAgent, rel); ok {
			t.Errorf("agent scope accepted %q as tier %q", rel, tier)
		}
		if tier, ok := classifyMemoryFile(scopeShared, rel); ok {
			t.Errorf("shared scope accepted %q as tier %q", rel, tier)
		}
	}
}
