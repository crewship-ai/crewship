package orchestrator

import (
	"context"
	"strings"
	"testing"
)

func TestWithoutMemoryWithholdsMemory(t *testing.T) {
	req := AgentRunRequest{MemoryEnabled: true, UserMessage: "hi"}.WithoutMemory()
	if req.MemoryEnabled || !req.withoutMemory {
		t.Fatalf("memory still on: enabled %v withheld %v", req.MemoryEnabled, req.withoutMemory)
	}
	if req.UserMessage != "hi" || req.SkipConvHistory || req.strictContentPrivacy {
		t.Fatal("the chat itself changed")
	}
	ctx := req.memoryContext(context.Background())
	// A withheld run never probes the sidecar (a nil provider would panic) and
	// gets no crewship-memory server.
	if memorySinkReady(ctx, nil, "container") {
		t.Fatal("the memory sink is ready for a withheld run")
	}
	out, err := injectMemoryMCPIntoClaudeJSON(`{"mcpServers":{}}`, "agent", memorySinkReady(ctx, nil, "container"))
	if err != nil || strings.Contains(out, MemoryMCPServerName) {
		t.Fatalf("memory MCP offered: %s %v", out, err)
	}
	if specs := injectMemoryMCP(nil, "agent", memorySinkReady(ctx, nil, "container")); len(specs) != 0 {
		t.Fatalf("memory MCP spec offered: %+v", specs)
	}
	if memoryWithheld(AgentRunRequest{}.memoryContext(context.Background())) {
		t.Fatal("an ordinary run is withheld")
	}
}
