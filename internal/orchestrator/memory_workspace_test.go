package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// stubWorkspaceProvider is a simple WorkspaceMemoryProvider whose
// resolution table is set per test. Returning nil for an unmapped
// workspace exercises the "no provider for this workspace" branch
// without changing the global wiring.
type stubWorkspaceProvider struct {
	readers map[string]WorkspaceMemoryReader
}

func (s *stubWorkspaceProvider) For(workspaceID string) WorkspaceMemoryReader {
	if s == nil || s.readers == nil {
		return nil
	}
	return s.readers[workspaceID]
}

// stubWorkspaceReader returns a fixed content + the byte count of the
// returned string. The orchestrator's block assembly handles framing,
// so the reader's job is just "here's what to render at this budget".
type stubWorkspaceReader struct {
	content string
}

func (s *stubWorkspaceReader) GetContext(budget int) (string, int) {
	if s == nil || s.content == "" || budget <= 0 {
		return "", 0
	}
	if len(s.content) > budget {
		// Mirror the WorkspaceMemory.GetContext truncation semantics —
		// not exactly the same chars but representative for tests.
		out := s.content[:budget-20] + "\n...(truncated)\n"
		return out, len(out)
	}
	return s.content, len(s.content)
}

// TestBuildWorkspaceMemoryBlock_NoProvider_Empty ensures the existing
// two-tier (agent + crew + pins) prompt path survives byte-for-byte
// when no WorkspaceMemoryProvider is wired — the workspace tier just
// disappears and its budget reclaims to agent.
func TestBuildWorkspaceMemoryBlock_NoProvider_Empty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	block, used := o.buildWorkspaceMemoryBlock("ws_test", 2000)
	if block != "" || used != 0 {
		t.Errorf("with no provider wired: block=%q used=%d, want empty", block, used)
	}
}

// TestBuildWorkspaceMemoryBlock_ProviderReturnsNilReader_Empty: provider
// is wired but has no memory for this specific workspace.
func TestBuildWorkspaceMemoryBlock_ProviderReturnsNilReader_Empty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	o.SetWorkspaceMemoryProvider(&stubWorkspaceProvider{readers: map[string]WorkspaceMemoryReader{
		"other_ws": &stubWorkspaceReader{content: "other content"},
	}})
	block, used := o.buildWorkspaceMemoryBlock("ws_test", 2000)
	if block != "" || used != 0 {
		t.Errorf("with provider but no reader for workspace: block=%q used=%d, want empty", block, used)
	}
}

// TestBuildWorkspaceMemoryBlock_RenderedWithFraming asserts the [WORKSPACE MEMORY]
// markers wrap the reader's content and the byte count is reported back
// so the caller's budget arithmetic stays honest.
func TestBuildWorkspaceMemoryBlock_RenderedWithFraming(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	o.SetWorkspaceMemoryProvider(&stubWorkspaceProvider{readers: map[string]WorkspaceMemoryReader{
		"ws_test": &stubWorkspaceReader{content: "shared workspace insight"},
	}})
	block, used := o.buildWorkspaceMemoryBlock("ws_test", 2000)
	if !strings.Contains(block, "[WORKSPACE MEMORY]") {
		t.Errorf("missing [WORKSPACE MEMORY] header in %q", block)
	}
	if !strings.Contains(block, "[END WORKSPACE MEMORY]") {
		t.Errorf("missing [END WORKSPACE MEMORY] footer in %q", block)
	}
	if !strings.Contains(block, "shared workspace insight") {
		t.Errorf("missing reader content in %q", block)
	}
	if used == 0 {
		t.Errorf("used = 0 with rendered content")
	}
}

// TestBuildWorkspaceMemoryBlock_SmallBudget_Empty: budget too small for
// the wrapper alone -> no block; caller's budget reallocation works.
func TestBuildWorkspaceMemoryBlock_SmallBudget_Empty(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})
	o := New(mc, newMemState(), slog.Default())
	o.SetWorkspaceMemoryProvider(&stubWorkspaceProvider{readers: map[string]WorkspaceMemoryReader{
		"ws_test": &stubWorkspaceReader{content: "something"},
	}})
	block, used := o.buildWorkspaceMemoryBlock("ws_test", 10)
	if block != "" || used != 0 {
		t.Errorf("with tiny budget: block=%q used=%d, want empty", block, used)
	}
}

// TestBuildMemoryContext_FourTiers_IncludesWorkspace: with a provider wired
// and content in all four tiers, the output contains every framing
// marker and every content slice.
func TestBuildMemoryContext_FourTiers_IncludesWorkspace(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/AGENT.md":          "long-term agent memory\n",
		"/crew/shared/.memory/CREW.md":                   "crew shared note\n",
		"/crew/shared/.memory/alpha-crew/topics/pins.md": "- **j_99** — never forget this\n",
	})
	o := New(mc, newMemState(), slog.Default())
	o.SetWorkspaceMemoryProvider(&stubWorkspaceProvider{readers: map[string]WorkspaceMemoryReader{
		"ws_test": &stubWorkspaceReader{content: "workspace-wide pattern from peer crew"},
	}})
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
	for _, want := range []string{
		"[AGENT MEMORY]",
		"[CREW SHARED MEMORY]",
		"[WORKSPACE MEMORY]",
		"[PINS]",
		"long-term agent memory",
		"crew shared note",
		"workspace-wide pattern from peer crew",
		"never forget this",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in 4-tier context output", want)
		}
	}
}

// TestBuildMemoryContext_NoWorkspaceProvider_NoRegression ensures that
// without a provider the old behaviour survives — same blocks, same
// markers, no spurious [WORKSPACE MEMORY] appears.
func TestBuildMemoryContext_NoWorkspaceProvider_NoRegression(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/agent-1/.memory/AGENT.md": "long-term\n",
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
	if strings.Contains(out, "[WORKSPACE MEMORY]") {
		t.Errorf("workspace tier must not appear without a provider: %q", out)
	}
	if !strings.Contains(out, "[AGENT MEMORY]") {
		t.Errorf("agent tier still expected: %q", out)
	}
}
