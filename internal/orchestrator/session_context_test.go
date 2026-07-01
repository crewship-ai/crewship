package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// prependSessionContext must wrap non-empty context and pass an empty context
// straight through so fresh sessions carry no empty wrapper.
func TestPrependSessionContext(t *testing.T) {
	if got := prependSessionContext("", "hello"); got != "hello" {
		t.Fatalf("empty context should pass through, got %q", got)
	}
	if got := prependSessionContext("   \n  ", "hello"); got != "hello" {
		t.Fatalf("whitespace-only context should pass through, got %q", got)
	}
	got := prependSessionContext("history here", "do the thing")
	if !strings.HasPrefix(got, sessionContextOpen) {
		t.Fatalf("missing open marker: %q", got)
	}
	if !strings.Contains(got, sessionContextClose+"\n\n"+"do the thing") {
		t.Fatalf("user message must follow the closed context block: %q", got)
	}
	if !strings.Contains(got, "history here") {
		t.Fatalf("context body missing: %q", got)
	}
}

// highMetrics forces both the nudge (entries well past nudgeThreshold) and the
// cost-awareness block (non-zero spend) to fire.
type highMetrics struct{}

func (highMetrics) EntriesSinceLastMemoryUpdate(_ context.Context, _, _ string) (int64, error) {
	return int64(nudgeThreshold) + 40, nil
}
func (highMetrics) AgentSpendLast24h(_ context.Context, _, _ string) (float64, int64, int64, error) {
	return 4.20, 9999, 12, nil
}

// The cache-prefix fix: MEMORY NUDGE and COST AWARENESS must no longer appear
// in the system-prompt memory block, even when metrics would trigger them —
// otherwise the cached prefix churns every turn. They must still be *available*
// via their builders so the run flow can route them into the user turn.
func TestMemoryContext_ExcludesVolatileNudgeAndCost(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/test-agent/.memory/AGENT.md": "durable note the agent wrote",
	})
	o := New(mc, newMemState(), slog.Default())
	o.SetMemoryMetrics(highMetrics{})

	req := AgentRunRequest{
		AgentID:       "ag1",
		WorkspaceID:   "ws1",
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	block := o.buildMemoryContext(context.Background(), req, 0)
	if strings.Contains(block, "[MEMORY NUDGE]") {
		t.Error("memory context still contains [MEMORY NUDGE] — breaks cache prefix")
	}
	if strings.Contains(block, "[COST AWARENESS]") {
		t.Error("memory context still contains [COST AWARENESS] — breaks cache prefix")
	}

	// The builders must still produce content for the volatile (user-turn) path.
	if nudge := o.buildNudgeBlock(context.Background(), req); !strings.Contains(nudge, "[MEMORY NUDGE]") {
		t.Errorf("buildNudgeBlock should still produce a nudge for the user turn, got %q", nudge)
	}
	if cost := o.buildCostAwarenessBlock(context.Background(), req); !strings.Contains(cost, "[COST AWARENESS]") {
		t.Errorf("buildCostAwarenessBlock should still produce a block for the user turn, got %q", cost)
	}
}
