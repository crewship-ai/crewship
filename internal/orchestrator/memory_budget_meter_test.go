package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// The model assembling this prompt is never otherwise told how much of
// its wake-time character budget it just spent, or whether a tier's
// content was silently dropped to fit (#1637 covers the drop; this is
// making the fact of it visible). Research on agent memory finds
// rendering that meter is the single largest lever measured in the area
// — the model self-manages instead of needing an eviction policy imposed
// on it. This file pins the wording: it has to match memory.write's
// existing usage meter (capUsage in internal/memory/tools.go) rather
// than invent a second format, and the truncation clause has to appear
// exactly when something was actually dropped — never as noise on a
// prompt that fit.

// TestRenderMemoryBudget_Table is the direct, table-driven test of the
// meter's formatting: a tier well under its allotment, one that nearly
// fills it without being cut, and one that was truncated. Only the last
// case may print "Truncated to fit".
func TestRenderMemoryBudget_Table(t *testing.T) {
	cases := []struct {
		name          string
		totalBudget   int
		stats         []memoryBudgetStat
		wantLines     []string
		wantTruncated bool
	}{
		{
			name:        "well under the limit",
			totalBudget: 15000,
			stats: []memoryBudgetStat{
				{label: "Agent", used: 300, budget: 8000, truncated: false},
			},
			wantLines: []string{
				"[MEMORY BUDGET]",
				"Agent: 300 of 8000 chars, 3%",
				"Total: 300 of 15000 chars, 2%",
				"[END MEMORY BUDGET]",
			},
			wantTruncated: false,
		},
		{
			name:        "near the limit, still fits",
			totalBudget: 15000,
			stats: []memoryBudgetStat{
				{label: "Pins", used: 140, budget: 1500, truncated: false},
				{label: "Agent", used: 7600, budget: 8000, truncated: false},
			},
			wantLines: []string{
				"Pins: 140 of 1500 chars, 9%",
				"Agent: 7600 of 8000 chars, 95%",
				"Total: 7740 of 15000 chars, 51%",
			},
			wantTruncated: false,
		},
		{
			name:        "overflow — truncated",
			totalBudget: 15000,
			stats: []memoryBudgetStat{
				{label: "Crew", used: 6000, budget: 6000, truncated: true},
				{label: "Agent", used: 8000, budget: 8000, truncated: false},
			},
			wantLines: []string{
				"Crew: 6000 of 6000 chars, 100%",
				"Agent: 8000 of 8000 chars, 100%",
				"Total: 14000 of 15000 chars, 93%",
			},
			wantTruncated: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderMemoryBudget(tc.totalBudget, tc.stats)
			for _, want := range tc.wantLines {
				if !strings.Contains(got, want) {
					t.Errorf("rendered meter missing %q\ngot:\n%s", want, got)
				}
			}
			hasTruncationNotice := strings.Contains(got, "Truncated to fit")
			if hasTruncationNotice != tc.wantTruncated {
				t.Errorf("truncation notice present=%v, want=%v\ngot:\n%s", hasTruncationNotice, tc.wantTruncated, got)
			}
		})
	}
}

// TestRenderMemoryBudget_MatchesWriteTimeWording pins the meter's wording
// to memory.write's existing overflow-guidance format (documented in
// docs/guides/agent-memory.mdx as e.g. "3900 of 4000 bytes, 97%") so the
// model reads one consistent usage-meter shape whether it's checking a
// write it just made or the snapshot it woke up with.
func TestRenderMemoryBudget_MatchesWriteTimeWording(t *testing.T) {
	got := renderMemoryBudget(4000, []memoryBudgetStat{
		{label: "Agent", used: 3900, budget: 4000, truncated: false},
	})
	if !strings.Contains(got, "3900 of 4000 chars, 97%") {
		t.Errorf("meter wording does not mirror memory.write's capUsage format (\"<used> of <cap> bytes/chars, <pct>%%\"):\n%s", got)
	}
}

// TestRenderMemoryBudget_NoStats_NoTruncationNoise guards against the
// meter inventing a truncation clause when nothing was allocated at all.
func TestRenderMemoryBudget_NoStats_NoTruncationNoise(t *testing.T) {
	got := renderMemoryBudget(15000, nil)
	if strings.Contains(got, "Truncated to fit") {
		t.Errorf("empty stats must never print a truncation notice:\n%s", got)
	}
	if !strings.Contains(got, "Total: 0 of 15000 chars, 0%") {
		t.Errorf("expected a zeroed total line:\n%s", got)
	}
}

// TestBuildMemoryContext_BudgetMeter_EndToEnd drives the meter through the
// real wake path (buildMemoryContext, via a mocked container) across
// under / near / overflow AGENT.md sizes, and checks the rendered
// [MEMORY BUDGET] block's Agent line matches what buildAgentMemoryBlock
// itself actually put in the prompt — not a hardcoded guess at the
// wrapper's byte accounting, which is why this discovers `full` and
// `tight` from the code under test rather than by hand.
func TestBuildMemoryContext_BudgetMeter_EndToEnd(t *testing.T) {
	const agentSlug = "agent-1"
	agentMDPath := "/crew/agents/" + agentSlug + "/.memory/AGENT.md"
	content := strings.Repeat("Fact about the project the agent has learned. ", 200) // ~9400 chars

	newOrch := func() *Orchestrator {
		mc := mockContainerForMemory(map[string]string{agentMDPath: content})
		return New(mc, newMemState(), slog.Default())
	}
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     agentSlug,
		AgentID:       "a1",
		MemoryEnabled: true,
	}

	// Discover the natural, un-truncated block size for this content so
	// the "near" and "overflow" budgets below are derived from the real
	// implementation instead of a magic number that would silently stop
	// meaning anything the day the wrapper overhead changes.
	probe := newOrch()
	fullBlock, _, fullTruncated := probe.buildAgentMemoryBlockDetailed(context.Background(), req, len(content)+2000, "2026-08-27")
	if fullTruncated {
		t.Fatalf("probe budget should be large enough to avoid truncation; block=%q", fullBlock)
	}
	full := len(fullBlock)

	cases := []struct {
		name          string
		budget        int
		wantTruncated bool
	}{
		{name: "well under the limit", budget: full * 5, wantTruncated: false},
		{name: "near the limit, still fits", budget: full + 20, wantTruncated: false},
		{name: "overflow — truncated", budget: full - 500, wantTruncated: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newOrch()
			out := o.buildMemoryContext(context.Background(), req, tc.budget)

			if !strings.Contains(out, "[MEMORY BUDGET]") {
				t.Fatalf("missing [MEMORY BUDGET] block:\n%s", out)
			}

			// Cross-check against the real block the agent tier produced
			// for this exact budget, so the meter's numbers are asserted
			// against ground truth rather than re-derived arithmetic that
			// could drift from the assembly code the same way twice.
			agentBlock, _, agentTruncated := o.buildAgentMemoryBlockDetailed(context.Background(), req, tc.budget, "2026-08-27")
			wantLine := memoryBudgetUsage(len(agentBlock), tc.budget)
			if !strings.Contains(out, "Agent: "+wantLine) {
				t.Errorf("meter's Agent line does not match the real block size (want %q):\n%s", wantLine, out)
			}
			if agentTruncated != tc.wantTruncated {
				t.Fatalf("test setup: budget=%d gave truncated=%v, want=%v — adjust the case", tc.budget, agentTruncated, tc.wantTruncated)
			}

			hasTruncationNotice := strings.Contains(out, "Truncated to fit")
			if hasTruncationNotice != tc.wantTruncated {
				t.Errorf("truncation notice present=%v, want=%v (budget=%d, used=%d):\n%s",
					hasTruncationNotice, tc.wantTruncated, tc.budget, len(agentBlock), out)
			}
			if tc.wantTruncated && !strings.Contains(out, "Truncated to fit: Agent") {
				t.Errorf("expected the truncation notice to name Agent:\n%s", out)
			}
		})
	}
}
