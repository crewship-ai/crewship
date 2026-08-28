package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// #1637 finding 1 (part 2): assembleSectionsEmitted's truncation branch
// used to slice a section's content at a raw byte offset —
// `section[:cut] + truncSuffix` — with no regard for where a multi-byte
// UTF-8 rune started. This product carries Czech text throughout, so a
// cut landing inside a 2-byte diacritic sequence severs a lead byte from
// its continuation byte and hands the model invalid UTF-8 inside a block
// it is told to trust as text. The fix (truncateUTF8) walks the cut
// point back to the nearest rune boundary.

// czechContent is dense multi-byte text: nearly every word carries at
// least one 2-byte UTF-8 rune (á, í, ž, ť, ů, ď, ó...), so a budget swept
// across a wide range is very likely to land a naive byte cut mid-rune
// somewhere in the range if the alignment fix regresses.
const czechContent = "Příliš žluťoučký kůň úpěl ďábelské ódy. Zvlášť tři přátelé škrábali stůl a četli knihu o vůni. "

// TestAssembleSectionsEmitted_TruncationNeverSplitsUTF8Rune sweeps a wide
// range of tight budgets over dense Czech content and asserts every
// truncated result is valid UTF-8. Before the fix this fails for the
// large majority of budgets in range, including reproducing the
// reviewer's exact "lead byte with its continuation severed" shape.
func TestAssembleSectionsEmitted_TruncationNeverSplitsUTF8Rune(t *testing.T) {
	content := strings.Repeat(czechContent, 40) // long enough to force truncation across the whole sweep
	sections := []memorySection{{label: "AGENT.md (long-term memory)", content: content}}

	invalidAt := []int{}
	for budget := 80; budget < 600; budget++ {
		block, _, _ := assembleSectionsEmitted("[AGENT MEMORY]", "[END AGENT MEMORY]", sections, budget)
		if !utf8.ValidString(block) {
			invalidAt = append(invalidAt, budget)
		}
	}
	if len(invalidAt) > 0 {
		t.Fatalf("truncation produced invalid UTF-8 at %d of the swept budgets (first few: %v)", len(invalidAt), firstN(invalidAt, 5))
	}
}

// TestAssembleSectionsEmitted_TruncationAtReproducedBudget pins the exact
// scenario the reviewer reproduced: a tight budget against Czech content
// that, under the old raw byte-slice cut, produced a malformed byte
// sequence injected into the [AGENT MEMORY] block sent to the model.
func TestAssembleSectionsEmitted_TruncationAtReproducedBudget(t *testing.T) {
	content := strings.Repeat(czechContent, 10)
	sections := []memorySection{{label: "AGENT.md (long-term memory)", content: content}}

	block, _, truncated := assembleSectionsEmitted("[AGENT MEMORY]", "[END AGENT MEMORY]", sections, 245)
	if !truncated {
		t.Fatal("expected truncation at a 245-byte budget against much larger content")
	}
	if !utf8.ValidString(block) {
		t.Fatalf("budget=245 produced invalid UTF-8:\n%q", block)
	}
}

// TestBuildAgentMemoryBlockDetailed_CzechTruncation_ValidUTF8 drives the
// same bug through the real wake path — buildAgentMemoryBlockDetailed via
// a mocked container serving an AGENT.md written in Czech — rather than
// only the lower-level assembleSectionsEmitted unit, so a regression
// anywhere between the container read and the rendered [AGENT MEMORY]
// block is caught.
func TestBuildAgentMemoryBlockDetailed_CzechTruncation_ValidUTF8(t *testing.T) {
	const agentSlug = "agent-cz"
	agentMDPath := "/crew/agents/" + agentSlug + "/.memory/AGENT.md"
	content := strings.Repeat(czechContent, 60) // ~2000 runes, ~2750 bytes, matching the reviewer's repro scale

	mc := mockContainerForMemory(map[string]string{agentMDPath: content})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     agentSlug,
		AgentID:       "a1",
		MemoryEnabled: true,
	}

	block, _, truncated := o.buildAgentMemoryBlockDetailed(context.Background(), req, 500, "2026-08-27")
	if !truncated {
		t.Fatal("expected truncation at a 500-byte budget against ~2750 bytes of content")
	}
	if !utf8.ValidString(block) {
		t.Fatalf("truncated [AGENT MEMORY] block is not valid UTF-8:\n%q", block)
	}
}

func firstN(xs []int, n int) []int {
	if len(xs) <= n {
		return xs
	}
	return xs[:n]
}
