package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestBuildMemoryToolsBlock_StructureAndEndpoints asserts the agent
// receives the three documented endpoints + their curl shapes, so a
// model that reads the block can construct a valid HTTP call
// without hallucinating the path or port.
func TestBuildMemoryToolsBlock_StructureAndEndpoints(t *testing.T) {
	block := buildMemoryToolsBlock()
	for _, want := range []string{
		"[MEMORY TOOLS]",
		"[END MEMORY TOOLS]",
		"http://127.0.0.1:9119",
		"memory_search",
		"memory_read",
		"memory_write",
		"/memory/search",
		"/memory/read",
		"/memory/write",
		// Search example must declare the scope: enum so the agent
		// knows it's not arbitrary.
		`"scope":"both"`,
		// Hybrid option documented — without this the agent never
		// learns it can ask for cross-corpus RRF recall.
		`"hybrid":true`,
		// Fallback header documented so the agent reads it and
		// understands degradation when IPC is missing.
		"X-Memory-Hybrid-Fallback",
		// Write example must mention the credential rejection so the
		// agent expects 422 on a leak, not a generic 500.
		"422",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q in [MEMORY TOOLS] block", want)
		}
	}
}

// TestBuildMemoryContext_AppendsToolsBlockAfterInstructions asserts
// that buildMemoryContext stitches the tools block in AFTER the
// instructions block, so an agent reading top-down sees memory state
// (AGENT.md content) before learning how to call the tools — which
// matches the cognitive flow: "what do I know" then "how do I add/
// recall more".
func TestBuildMemoryContext_AppendsToolsBlockAfterInstructions(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/AGENT.md": "a fact\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "agent-1",
		AgentID:       "a1",
		CrewID:        "crew1",
		CrewSlug:      "alpha-crew",
		WorkspaceID:   "ws_test",
		MemoryEnabled: true,
	}
	out := o.buildMemoryContext(context.Background(), req, 0)

	instIdx := strings.Index(out, "[MEMORY INSTRUCTIONS]")
	toolsIdx := strings.Index(out, "[MEMORY TOOLS]")
	if instIdx < 0 {
		t.Fatalf("missing [MEMORY INSTRUCTIONS] marker")
	}
	if toolsIdx < 0 {
		t.Fatalf("missing [MEMORY TOOLS] marker")
	}
	if toolsIdx < instIdx {
		t.Errorf("tools (%d) should appear after instructions (%d)", toolsIdx, instIdx)
	}
}

// TestBuildMemoryContext_ToolsBlockAbsentOnEmptyTiers — when no
// memory files exist the function early-returns the instructions
// block alone, deliberately WITHOUT the [MEMORY TOOLS] surface.
// The tools block only matters once the agent has at least one
// tier of memory to consult; surfacing it on the empty path
// would add a surface that exists in name only.
//
// Renamed from ...SurvivesEmptyTiers because the previous name
// implied the opposite contract — locking the test name + the
// assertion together so a future refactor that flips one but not
// the other fails to compile rather than passing silently.
func TestBuildMemoryContext_ToolsBlockAbsentOnEmptyTiers(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "agent-1",
		MemoryEnabled: true,
	}
	out := o.buildMemoryContext(context.Background(), req, 0)
	// Fresh-agent path: instructions appear, but tools block may
	// or may not depending on the implementation. Locking the
	// expectation here forces the implementation to be consistent.
	if !strings.Contains(out, "[MEMORY INSTRUCTIONS]") {
		t.Errorf("instructions block must appear even with no memory files")
	}
	// Lock the current contract: the early-return branch deliberately
	// skips [MEMORY TOOLS] because the surface only matters once the
	// agent has at least one tier of memory to consult. Asserting
	// (rather than logging) here forces a future implementation
	// change to be deliberate — adding tools to the empty path is
	// fine, but it should be a code change paired with this assertion
	// flipping, not a silent test passing on either behaviour.
	if strings.Contains(out, "[MEMORY TOOLS]") {
		t.Errorf("[MEMORY TOOLS] should NOT appear on empty-tier early-return; the contract is 'tools only when there is memory to query'")
	}
}
