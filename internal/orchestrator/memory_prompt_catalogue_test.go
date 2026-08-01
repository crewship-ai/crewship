package orchestrator

import (
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// toolMention matches a tool-shaped token in prompt text:
// lowercase.dotted, at least three characters either side of the dot.
//
// It is deliberately shape-based rather than sentence-based. The prompt
// gets reworded — that is the point of prompt text — and a test that
// grepped for today's sentences ("run memory.search for the project you
// are picking up") would go green the moment someone improved the
// wording, which is exactly when it needs to be watching. Matching the
// token shape means the check survives any rewrite that still names a
// tool.
//
// The three-character floor is what keeps file and prose tokens out:
// "AGENT.md" / "pins.md" / "daily/2026-08-01.md" all fail (".md" is two
// characters, and a leading capital is not [a-z]), and so do "e.g." and
// "i.e.". knownNonTools below is the escape hatch for anything that
// slips through later — adding to it is a deliberate act, unlike a
// silently-non-matching regex.
var toolMention = regexp.MustCompile(`\b[a-z][a-z0-9_]{2,}\.[a-z][a-z0-9_]{2,}\b`)

var knownNonTools = map[string]bool{}

// promptSurfaces returns every block of prompt text this package writes
// for the model, with a label for the failure message. If a new block is
// added that names tools, add it here — the catalogue check is only as
// wide as this list.
func promptSurfaces(today string) map[string]string {
	out := map[string]string{
		"renderMemoryInstructions": renderMemoryInstructions(today),
	}
	for _, ev := range []struct {
		name string
		ev   gapEvidence
	}{
		{"memoryGap/notes-below", gapNotesBelow},
		{"memoryGap/notes-out-of-window", gapNotesOutOfWindow},
		{"memoryGap/notes-withheld", gapNotesWithheld},
	} {
		g := memoryGap{lastActive: "2026-07-25", days: 7, evidence: ev.ev}
		out[ev.name] = g.render(today)
	}
	return out
}

// TestPromptNamesOnlyAdvertisedTools is the regression test for #1651.
//
// The [MEMORY GAP] block is the product's best memory feature — it tells
// a woken agent, in a sentence the model reads, that time has passed and
// what to do about it. It shipped naming two recall tools. One of them,
// conversation.search, was in no tools/list the model ever saw and had
// no backend wired, so the instruction resolved to "call a tool that
// does not exist here".
//
// The failure mode is drift between two files nobody edits together: the
// prompt (this package) and the tool catalogue (memory.AdvertisedTools,
// rendered by the sidecar's MCP tools/list). Only a test that reads both
// can prevent it, so this one does.
func TestPromptNamesOnlyAdvertisedTools(t *testing.T) {
	advertised := map[string]bool{}
	for _, name := range memory.AdvertisedTools() {
		advertised[name] = true
	}
	if len(advertised) == 0 {
		t.Fatal("memory.AdvertisedTools() is empty — nothing to check against")
	}

	for label, text := range promptSurfaces("2026-08-01") {
		if strings.TrimSpace(text) == "" {
			t.Errorf("prompt surface %s rendered empty — the fixture no longer exercises it", label)
			continue
		}
		found := map[string]bool{}
		named := 0
		for _, m := range toolMention.FindAllString(text, -1) {
			if knownNonTools[m] {
				continue
			}
			found[m] = true
			if advertised[m] {
				named++
				continue
			}
			t.Errorf(`%s names the tool %q, which memory.AdvertisedTools() does not advertise.

The model is being told to call something its tools/list never shows it. Either
add %q to the catalogue (and wire a backend the in-container dispatcher can
reach), or stop naming it in the prompt. Advertised today: %v`,
				label, m, m, memory.AdvertisedTools())
		}

		// Per surface, not per corpus: without this the check passes as
		// long as SOME block still names a tool, so a rewrite that turns
		// one block's instruction into prose ("use the memory search
		// tool") slips through — and a tool the model cannot name is a
		// tool it will not call. It also guards the regex itself: if the
		// naming convention ever changes shape, every surface goes to
		// zero at once rather than the whole test quietly matching
		// nothing.
		if named == 0 {
			t.Errorf(`%s names no advertised tool (tokens found: %v).

Either its recall instruction was dropped — the agent is told what happened but
not what to do — or it now refers to tools in prose that toolMention cannot
match, which the model cannot act on either.`, label, keysOf(found))
		}
	}
}

// TestPromptCatalogueCheck_CatchesAnUnadvertisedTool proves the check
// above can actually fail. A test whose regex quietly matches nothing
// passes forever; this one feeds it a surface naming a tool that is not
// in the catalogue and asserts the extraction sees it.
func TestPromptCatalogueCheck_CatchesAnUnadvertisedTool(t *testing.T) {
	const text = "Before you start, run memory.search for the task and\n" +
		"conversation.search for what was last discussed. See AGENT.md,\n" +
		"pins.md and daily/2026-08-01.md — e.g. the last entry."

	got := map[string]bool{}
	for _, m := range toolMention.FindAllString(text, -1) {
		got[m] = true
	}
	if !got["memory.search"] || !got["conversation.search"] {
		t.Errorf("extraction missed a tool mention: %v", keysOf(got))
	}
	for _, notATool := range []string{"AGENT.md", "pins.md", "daily/2026-08-01.md", "e.g", "e.g."} {
		if got[notATool] {
			t.Errorf("extraction treated %q as a tool name", notATool)
		}
	}
	if len(got) != 2 {
		t.Errorf("expected exactly the two tool mentions, got %v", keysOf(got))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
