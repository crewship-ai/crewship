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
