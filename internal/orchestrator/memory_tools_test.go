package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestBuildMemoryContext_NoCurlToolsBlock_PostZ1 locks in PR-Z Z.1: the
// curl-based [MEMORY TOOLS] system-prompt block is gone. PR-A F1
// replaces it with native function-calling tools per CLI adapter; until
// then the agent has only the boot snapshot for mid-session recall.
//
// This test stays as the tombstone — flipping it forces a future change
// to be deliberate. If F1 reintroduces a prompt-level surface (e.g. a
// minimal "tools available" hint), update the assertion AND the
// implementation together so they fail to compile in lockstep.
func TestBuildMemoryContext_NoCurlToolsBlock_PostZ1(t *testing.T) {
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

	cases := []struct {
		name   string
		banned string
	}{
		{name: "start marker", banned: "[MEMORY TOOLS]"},
		{name: "end marker", banned: "[END MEMORY TOOLS]"},
		{name: "loopback ip endpoint", banned: "127.0.0.1:9119"},
		{name: "localhost endpoint", banned: "localhost:9119"},
		{name: "search route", banned: "/memory/search"},
		{name: "read route", banned: "/memory/read"},
		{name: "write route", banned: "/memory/write"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(out, tc.banned) {
				t.Errorf("PR-Z Z.1: %q must not appear in memory context after curl tools block removal", tc.banned)
			}
		})
	}

	// [MEMORY INSTRUCTIONS] block (the higher-level "you have memory"
	// guidance, not the curl tool surface) MUST still appear. It's the
	// non-tool-specific framing that survives both Z.1 and F1.
	if !strings.Contains(out, "[MEMORY INSTRUCTIONS]") {
		t.Errorf("[MEMORY INSTRUCTIONS] must still appear post-Z.1; only the curl tools block is gone")
	}
}
