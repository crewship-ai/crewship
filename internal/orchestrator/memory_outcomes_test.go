package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// lessonsFixtureYAML is a hand-written lessons.md that mirrors the
// format consolidate.WriteCrewLesson actually emits. We use raw YAML
// (not a real round-trip through the writer) so the orchestrator
// test stays independent of the consolidate package.
const lessonsFixtureYAML = `# Lessons learned by this agent.
# Append-only by ID.

entries:
    - id: mission_outcome_m1
      kind: positive
      captured_at: 2026-05-22T10:00:00Z
      source: mission_outcome
      rule: "ENG-1 completed: ping google.com 5 times"
      context: "COMPLETED · LEAD=eva"
    - id: mission_outcome_m2
      kind: negative
      captured_at: 2026-05-22T11:00:00Z
      source: mission_outcome
      rule: "DEV-4 failed: trace DNS resolution"
      context: "FAILED · LEAD=ondrej"
    - id: mission_outcome_m3
      kind: neutral
      captured_at: 2026-05-22T12:00:00Z
      source: mission_outcome
      rule: "QUA-2 cancelled: log parser"
      context: "CANCELLED · LEAD=beacon"
`

// TestBuildMemoryContext_LEAD_SeesCrewOutcomes verifies the
// [CREW OUTCOMES] section the F4.5 PRD adds to the LEAD boot prompt.
// When a crew has a lessons.md populated by the mission-outcomes
// hook, a LEAD agent's boot context must render entries inside the
// existing [CREW SHARED MEMORY] block.
func TestBuildMemoryContext_LEAD_SeesCrewOutcomes(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/CREW.md":                "# Crew\nDeploy via GitHub Actions.",
		"/crew/shared/.memory/daily/" + today + ".md": "# Today\nReviewed PR #42.",
		"/crew/shared/.memory/lessons.md":             lessonsFixtureYAML,
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "eva",
		ContainerID:   "c-lead",
		MemoryEnabled: true,
		AgentRole:     "LEAD",
		CrewID:        "crew-1",
	}

	result := o.buildMemoryContext(context.Background(), req, 0)

	if !strings.Contains(result, "[CREW SHARED MEMORY]") {
		t.Fatal("missing [CREW SHARED MEMORY] block; got:\n" + result)
	}
	for _, want := range []string{
		"Crew outcomes",   // section label
		"ENG-1 completed", // positive entry
		"DEV-4 failed",    // negative entry
		"QUA-2 cancelled", // neutral entry
		"LEAD=eva",        // context attribution
	} {
		if !strings.Contains(result, want) {
			t.Errorf("LEAD prompt missing %q\n--- got ---\n%s", want, result)
		}
	}
}

// TestBuildMemoryContext_NonLEAD_NoCrewOutcomes pins the role gate:
// AGENT-role members already get CREW.md and the daily log; surfacing
// the operational outcomes digest would burn tokens on every agent
// run without commensurate signal. AGENT-role can still pull lessons
// mid-session via memory.read tier=lessons if they ask.
func TestBuildMemoryContext_NonLEAD_NoCrewOutcomes(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/CREW.md":                "# Crew\nDeploy notes.",
		"/crew/shared/.memory/daily/" + today + ".md": "# Today\nStandup.",
		"/crew/shared/.memory/lessons.md":             lessonsFixtureYAML,
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "daniel",
		ContainerID:   "c-agent",
		MemoryEnabled: true,
		AgentRole:     "AGENT",
		CrewID:        "crew-1",
	}

	result := o.buildMemoryContext(context.Background(), req, 0)

	// CREW.md is still visible.
	if !strings.Contains(result, "Deploy notes") {
		t.Error("AGENT-role lost regular CREW.md content")
	}
	// Outcomes section must NOT be rendered.
	for _, hidden := range []string{"Crew outcomes", "ENG-1 completed", "DEV-4 failed"} {
		if strings.Contains(result, hidden) {
			t.Errorf("AGENT-role prompt unexpectedly contains %q", hidden)
		}
	}
}

// TestBuildMemoryContext_LEAD_NoLessonsFile — a fresh crew has no
// lessons.md yet. The block must render the existing CREW.md/daily
// content unchanged (no "Crew outcomes" header, no empty section).
// This guards against a regression where the section label appears
// even when there's nothing to list.
func TestBuildMemoryContext_LEAD_NoLessonsFile(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/CREW.md":                "# Crew\nFresh team.",
		"/crew/shared/.memory/daily/" + today + ".md": "# Today\nOnboarding.",
		// NO lessons.md
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "lead",
		ContainerID:   "c-lead",
		MemoryEnabled: true,
		AgentRole:     "LEAD",
		CrewID:        "crew-fresh",
	}

	result := o.buildMemoryContext(context.Background(), req, 0)

	if !strings.Contains(result, "Fresh team") {
		t.Error("CREW.md missing in fresh-crew LEAD prompt")
	}
	if strings.Contains(result, "Crew outcomes") {
		t.Error("outcomes section rendered with no lessons file present")
	}
}

// TestBuildMemoryContext_LEAD_OutcomesFiltersToMissionOutcomeSource —
// lessons.md may contain entries from other sources (skill_promote,
// negative_learning at the agent tier, manual). The outcomes section
// must surface ONLY entries with source=mission_outcome — surfacing
// arbitrary lessons would confuse the LEAD by mixing per-agent
// learning into a crew-level operational digest.
func TestBuildMemoryContext_LEAD_OutcomesFiltersToMissionOutcomeSource(t *testing.T) {
	mixedLessons := `entries:
    - id: mission_outcome_yes
      kind: positive
      captured_at: 2026-05-22T10:00:00Z
      source: mission_outcome
      rule: "ENG-X completed: should appear"
      context: "COMPLETED · LEAD=eva"
    - id: manual_no
      kind: positive
      captured_at: 2026-05-22T10:30:00Z
      source: manual
      rule: "Manual lesson: should NOT appear in outcomes section"
      context: "operator note"
    - id: skill_promote_no
      kind: positive
      captured_at: 2026-05-22T11:00:00Z
      source: skill_promote
      rule: "Skill X promoted: should NOT appear in outcomes section"
      context: ""
`
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/CREW.md":    "# Crew",
		"/crew/shared/.memory/lessons.md": mixedLessons,
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "eva",
		ContainerID:   "c-lead",
		MemoryEnabled: true,
		AgentRole:     "LEAD",
		CrewID:        "crew-mix",
	}

	result := o.buildMemoryContext(context.Background(), req, 0)

	if !strings.Contains(result, "ENG-X completed") {
		t.Error("mission_outcome entry must appear in outcomes section")
	}
	if strings.Contains(result, "Manual lesson") {
		t.Error("non-mission-outcome source (manual) leaked into outcomes section")
	}
	if strings.Contains(result, "Skill X promoted") {
		t.Error("non-mission-outcome source (skill_promote) leaked into outcomes section")
	}
}
