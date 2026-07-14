package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestBuildPinsBlock_ReadsContainerPath asserts that buildPinsBlock
// resolves the correct in-container path
// (/crew/shared/.memory/{crew_slug}/topics/pins.md) and renders the
// content inside a [PINS] block.
func TestBuildPinsBlock_ReadsContainerPath(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/shared/.memory/alpha-crew/topics/pins.md": "- **j_42** (mission.status_change, 2026-05-10) — auth migration unblocked\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID: "c1",
		AgentSlug:   "agent-1",
		AgentID:     "a1",
		CrewID:      "crew1",
		CrewSlug:    "alpha-crew",
		WorkspaceID: "ws1",
	}
	block, used := o.buildPinsBlock(context.Background(), req, 2000)
	if used == 0 {
		t.Fatalf("expected non-zero usage when pins.md exists")
	}
	if !strings.Contains(block, "[PINS]") {
		t.Errorf("missing [PINS] header in %q", block)
	}
	if !strings.Contains(block, "[END PINS]") {
		t.Errorf("missing [END PINS] footer in %q", block)
	}
	if !strings.Contains(block, "auth migration unblocked") {
		t.Errorf("pins content not present in block: %q", block)
	}
}

// TestBuildPinsBlock_NoCrewSlug_Empty: missing crew slug must not
// crash and must return empty (no probing the container with a half-
// resolved path).
func TestBuildPinsBlock_NoCrewSlug_Empty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{ContainerID: "c1", CrewID: "crew1"} // no CrewSlug
	block, used := o.buildPinsBlock(context.Background(), req, 2000)
	if block != "" || used != 0 {
		t.Fatalf("expected empty block + zero usage, got %q / %d", block, used)
	}
}

// TestBuildPinsBlock_FileMissing_Empty: container has no pins.md →
// graceful empty.
func TestBuildPinsBlock_FileMissing_Empty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{}) // no entries
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID: "c1",
		AgentSlug:   "agent-1",
		CrewID:      "crew1",
		CrewSlug:    "alpha-crew",
	}
	block, used := o.buildPinsBlock(context.Background(), req, 2000)
	if block != "" || used != 0 {
		t.Errorf("expected empty for missing pins.md, got %q / %d", block, used)
	}
}

// TestBuildAgentMemoryBlock_InjectsAgentPins asserts #1134: a
// memory.write tier=pins durable file at
// /crew/agents/{slug}/.memory/pins.md is force-injected into the
// [AGENT MEMORY] block — deterministically, without any model tool
// call. This is distinct from the operator-journal [PINS] block
// (buildPinsBlock), which reads a different file.
func TestBuildAgentMemoryBlock_InjectsAgentPins(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/pins.md":  "- deploy key rotates monthly — never hardcode\n",
		"/crew/agents/agent-1/.memory/AGENT.md": "long-term agent memory\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID: "c1",
		AgentSlug:   "agent-1",
		AgentID:     "a1",
		CrewID:      "crew1",
		CrewSlug:    "alpha-crew",
	}
	block := o.buildAgentMemoryBlock(context.Background(), req, 4000, "2026-07-14")
	if !strings.Contains(block, "[AGENT MEMORY]") {
		t.Fatalf("missing [AGENT MEMORY] header in %q", block)
	}
	if !strings.Contains(block, "deploy key rotates monthly") {
		t.Errorf("agent pin content not injected into [AGENT MEMORY]: %q", block)
	}
	// Pins must sit ahead of AGENT.md so an aggressive truncation pass
	// (first sections survive) keeps them — "pinned = always in context".
	pinIdx := strings.Index(block, "deploy key rotates monthly")
	agentIdx := strings.Index(block, "long-term agent memory")
	if pinIdx == -1 || agentIdx == -1 || pinIdx > agentIdx {
		t.Errorf("agent pin must precede AGENT.md content; pinIdx=%d agentIdx=%d", pinIdx, agentIdx)
	}
}

// TestBuildAgentMemoryBlock_AgentPinsOnly_RendersBlock: even with no
// AGENT.md / daily / BRIEF, a lone agent pin file still renders the
// [AGENT MEMORY] block — proving the injection is independent of the
// other tiers (the crux of the #1134 contract).
func TestBuildAgentMemoryBlock_AgentPinsOnly_RendersBlock(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/pins.md": "- always run migrations forward-only\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{ContainerID: "c1", AgentSlug: "agent-1", AgentID: "a1", CrewID: "crew1"}
	block := o.buildAgentMemoryBlock(context.Background(), req, 4000, "2026-07-14")
	if !strings.Contains(block, "[AGENT MEMORY]") {
		t.Fatalf("lone agent pin file should still render [AGENT MEMORY]: %q", block)
	}
	if !strings.Contains(block, "forward-only") {
		t.Errorf("agent pin content missing: %q", block)
	}
}

// TestBuildMemoryContext_IncludesAgentPins asserts the integration:
// an agent-tool pin is present in the full assembled context even when
// the model never issues a memory.read (deterministic force-injection).
func TestBuildMemoryContext_IncludesAgentPins(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/pins.md": "- never delete the audit log\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "agent-1",
		AgentID:       "a1",
		CrewID:        "crew1",
		CrewSlug:      "alpha-crew",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}
	out := o.buildMemoryContext(context.Background(), req, 0)
	if !strings.Contains(out, "never delete the audit log") {
		t.Errorf("agent pin not force-injected into memory context: %q", out)
	}
}

// TestBuildMemoryContext_IncludesPins asserts the integration: when
// pins.md exists in the container, buildMemoryContext stacks [PINS]
// alongside the existing tiers.
func TestBuildMemoryContext_IncludesPins(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/AGENT.md":          "long-term agent memory\n",
		"/crew/shared/.memory/CREW.md":                   "crew shared note\n",
		"/crew/shared/.memory/alpha-crew/topics/pins.md": "- **j_99** — never forget this\n",
	})
	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		ContainerID:   "c1",
		AgentSlug:     "agent-1",
		AgentID:       "a1",
		CrewID:        "crew1",
		CrewSlug:      "alpha-crew",
		WorkspaceID:   "ws1",
		MemoryEnabled: true,
	}
	out := o.buildMemoryContext(context.Background(), req, 0)
	for _, want := range []string{
		"[AGENT MEMORY]",
		"[CREW SHARED MEMORY]",
		"[PINS]",
		"never forget this",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in context output", want)
		}
	}
}
