package orchestrator

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// crewLessonEntry is the subset of consolidate.LessonEntry that this
// package needs to render the outcomes section. Kept package-local
// (rather than importing the consolidate type) so the orchestrator
// doesn't pick up a runtime dependency on the writer package — the
// boot prompt assembler reads YAML, it doesn't write lessons.
//
// Field names mirror the YAML produced by consolidate.WriteCrewLesson
// (id, kind, captured_at, source, rule, context). The unmarshaler
// ignores unknown fields, so a future writer addition is a non-breaking
// change for this reader.
type crewLessonEntry struct {
	ID         string `yaml:"id"`
	Kind       string `yaml:"kind"`
	CapturedAt string `yaml:"captured_at"`
	Source     string `yaml:"source"`
	Rule       string `yaml:"rule"`
	Context    string `yaml:"context"`
}

type crewLessonsFile struct {
	Entries []crewLessonEntry `yaml:"entries"`
}

// renderCrewOutcomes turns a raw lessons.md body into the bullet-list
// text that appears under the "Crew outcomes" section of the LEAD's
// boot prompt. Returns the empty string when:
//
//   - the body is empty (no lessons.md file)
//   - the body parses but contains no entries
//   - the body parses but no entries have source=mission_outcome
//
// max caps how many entries are rendered (most-recent-last in the
// file order, which mirrors capture order because
// consolidate.WriteCrewLesson appends in capture order). When the
// entries count exceeds max, the OLDEST entries are dropped so the
// LEAD always sees the freshest signal.
//
// Output shape per entry, one line each:
//
//	✓ ENG-1 completed: ping google.com (COMPLETED · LEAD=eva)
//	✗ DEV-4 failed: trace DNS (FAILED · LEAD=ondrej)
//	· QUA-2 cancelled: log parser (CANCELLED · LEAD=beacon)
//
// The leading glyph (✓ / ✗ / ·) encodes polarity so the model can
// scan the section without parsing the body. Bytes overhead per
// glyph is small but it's the densest signal-per-token format we
// can give the LEAD.
func renderCrewOutcomes(rawYAML string, max int) string {
	if strings.TrimSpace(rawYAML) == "" {
		return ""
	}
	var parsed crewLessonsFile
	if err := yaml.Unmarshal([]byte(rawYAML), &parsed); err != nil {
		// Malformed lessons.md — render nothing rather than partial
		// garbage. The writer's atomic temp+rename means a half-
		// written file shouldn't happen in practice; this branch is
		// a safety net for an operator-edited file with bad YAML.
		return ""
	}

	filtered := make([]crewLessonEntry, 0, len(parsed.Entries))
	for _, e := range parsed.Entries {
		if e.Source != "mission_outcome" {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		return ""
	}

	// Cap to last `max`. The on-disk order is capture order
	// (consolidate.WriteCrewLesson appends), so slicing from the tail
	// gives "last N captured".
	if len(filtered) > max {
		filtered = filtered[len(filtered)-max:]
	}

	var b strings.Builder
	for _, e := range filtered {
		glyph := outcomeGlyph(e.Kind)
		// Defensive trimming — operator-edited entries may have
		// stray whitespace. The renderer should not propagate it
		// into the LEAD prompt where every token costs.
		rule := strings.TrimSpace(e.Rule)
		ctx := strings.TrimSpace(e.Context)
		if rule == "" {
			continue
		}
		if ctx != "" {
			fmt.Fprintf(&b, "%s %s (%s)\n", glyph, rule, ctx)
		} else {
			fmt.Fprintf(&b, "%s %s\n", glyph, rule)
		}
	}
	return b.String()
}

// outcomeGlyph maps a lesson kind to a one-character polarity marker.
// Unknown kinds get the neutral mid-dot so a malformed entry still
// renders something the model can scan past.
func outcomeGlyph(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "positive":
		return "✓"
	case "negative":
		return "✗"
	case "neutral":
		return "·"
	default:
		return "·"
	}
}

// isLeadRole normalises the AgentRunRequest.AgentRole field for the
// F4.5 outcomes gate. The DB and orchestrator pass roles as upper-
// case ("LEAD" / "AGENT"); the per-run lower-cased serialization in
// SidecarMemoryConfig.AgentRole is a separate field used only inside
// the sidecar process, not on this code path. We accept either form
// to keep the gate robust against a caller passing the wrong case.
func isLeadRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "LEAD")
}
