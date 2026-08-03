package memport

import "testing"

// The one nested shape the importer accepts is the crew's own
// <slug>/topics/pins.md. "Some directory called topics" is not that
// shape, and accepting it would let an import invent directories inside
// the memory tree.
func TestCheckImportPathCrewTopicsShape(t *testing.T) {
	tests := []struct {
		rel     string
		allowed bool
	}{
		{"engineering/topics/pins.md", true},
		{"eng-team_2/topics/pins.md", true},
		{"AGENT.md", true},
		{"daily/2026-08-01.md", true},
		{"peers/pavel.md", true},

		{"../evil/topics/pins.md", false},
		{".hidden/topics/pins.md", false},
		{"a/b/topics/pins.md", false},
		{"engineering/topics/AGENT.md", false},
		{"engineering/nottopics/pins.md", false},
		{"lessons.md", false},
		{"learned-ops.md", false},
		{".quarantine/abc.md", false},
		{"daily/2026-08-01/notes.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			_, refusal := checkImportPath(tt.rel, ScopeCrew)
			if tt.allowed && refusal != "" {
				t.Errorf("checkImportPath(%q) refused: %s", tt.rel, refusal)
			}
			if !tt.allowed && refusal == "" {
				t.Errorf("checkImportPath(%q) was accepted", tt.rel)
			}
		})
	}
}
