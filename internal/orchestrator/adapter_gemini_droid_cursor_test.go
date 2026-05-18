package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// adapter_gemini.go / adapter_droid.go / adapter_cursor.go —
// SetupSystemPrompt + WriteMCPConfig wrappers.
//
// All three adapters follow the same shape: thin wrappers that delegate
// to writeCanonicalMemoryFiles / writeMCP* and attach a per-adapter
// "<name> adapter ...: %w" error prefix. The opencode test pinned that
// pattern for one adapter; this file covers the other three so every
// CLI-adapter setup error is operator-actionable.
//
// Reuses adapterTestContainer + quietAdapterLogger from
// adapter_opencode_test.go (same package).
// ---------------------------------------------------------------------------

// --- Compile-time interface satisfaction (catches CLIAdapter drift) ---
var (
	_ CLIAdapter = geminiAdapter{}
	_ CLIAdapter = droidAdapter{}
	_ CLIAdapter = cursorAdapter{}
)

// =============================================================================
// geminiAdapter
// =============================================================================

func TestGeminiAdapter_SetupSystemPrompt_HappyPath(t *testing.T) {
	fake := &adapterTestContainer{}
	err := geminiAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-g", AgentRunRequest{SystemPrompt: "be terse"},
		"/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Fatalf("SetupSystemPrompt: %v", err)
	}
	if fake.execCalls < 5 {
		t.Errorf("execCalls = %d, want ≥ 5 (canonical memory files)", fake.execCalls)
	}
}

func TestGeminiAdapter_SetupSystemPrompt_AllWritesFail_WrapsWithAdapterName(t *testing.T) {
	want := errors.New("daemon offline")
	fake := &adapterTestContainer{execErr: want}
	a := geminiAdapter{}
	err := a.SetupSystemPrompt(
		context.Background(), fake, "ct-gf", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "gemini adapter setup system prompt") {
		t.Errorf("err = %v, want \"gemini adapter setup system prompt\" prefix", err)
	}
}

func TestGeminiAdapter_WriteMCPConfig_EmptyMCP_ShortCircuitsToNil(t *testing.T) {
	fake := &adapterTestContainer{}
	a := geminiAdapter{}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-gm", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Errorf("WriteMCPConfig on empty MCP = %v, want nil", err)
	}
	if fake.execCalls != 0 {
		t.Errorf("empty MCP triggered %d Exec calls; expected 0", fake.execCalls)
	}
}

func TestGeminiAdapter_WriteMCPConfig_WrapsContainerWriteFailure(t *testing.T) {
	want := errors.New("write refused")
	fake := &adapterTestContainer{execErr: want}
	a := geminiAdapter{}
	req := AgentRunRequest{
		MCPServers: []MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}},
		},
	}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-gmf", req, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error on container failure")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "gemini adapter write MCP config") {
		t.Errorf("err = %v, want \"gemini adapter write MCP config\" prefix", err)
	}
}

func TestGeminiAdapter_WriteMCPConfig_HappyPath_TargetsGeminiSettings(t *testing.T) {
	// writeMCPGemini writes to .gemini/settings.json — pin the per-CLI
	// file path so a refactor that swapped writers doesn't silently
	// land MCP config in the wrong location.
	fake := &adapterTestContainer{}
	a := geminiAdapter{}
	req := AgentRunRequest{
		MCPServers: []MCPServerConfig{
			{Name: "fs", Transport: "stdio", Command: "npx"},
		},
	}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-gh", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	found := false
	for _, s := range fake.execScripts {
		if strings.Contains(s, ".gemini/settings.json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no exec script targeted .gemini/settings.json; got %v", fake.execScripts)
	}
}

// =============================================================================
// droidAdapter
// =============================================================================

func TestDroidAdapter_SetupSystemPrompt_HappyPath(t *testing.T) {
	fake := &adapterTestContainer{}
	err := droidAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-d", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Fatalf("SetupSystemPrompt: %v", err)
	}
	if fake.execCalls < 5 {
		t.Errorf("execCalls = %d, want ≥ 5", fake.execCalls)
	}
}

func TestDroidAdapter_SetupSystemPrompt_AllWritesFail_WrapsWithAdapterName(t *testing.T) {
	want := errors.New("boom")
	fake := &adapterTestContainer{execErr: want}
	a := droidAdapter{}
	err := a.SetupSystemPrompt(
		context.Background(), fake, "ct-df", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "droid adapter setup system prompt") {
		t.Errorf("err = %v, want \"droid adapter setup system prompt\" prefix", err)
	}
}

func TestDroidAdapter_WriteMCPConfig_EmptyMCP_ShortCircuitsToNil(t *testing.T) {
	fake := &adapterTestContainer{}
	a := droidAdapter{}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-dm", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if fake.execCalls != 0 {
		t.Errorf("empty MCP triggered %d Exec calls; expected 0", fake.execCalls)
	}
}

func TestDroidAdapter_WriteMCPConfig_WrapsFailure(t *testing.T) {
	want := errors.New("nope")
	fake := &adapterTestContainer{execErr: want}
	a := droidAdapter{}
	req := AgentRunRequest{MCPServers: []MCPServerConfig{
		{Name: "fs", Transport: "stdio", Command: "npx"},
	}}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-dmf", req, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "droid adapter write MCP config") {
		t.Errorf("err = %v, want \"droid adapter write MCP config\" prefix", err)
	}
}

func TestDroidAdapter_WriteMCPConfig_HappyPath_TargetsFactoryMCP(t *testing.T) {
	// writeMCPDroid writes under .factory/ — pin the per-CLI file path.
	fake := &adapterTestContainer{}
	a := droidAdapter{}
	req := AgentRunRequest{MCPServers: []MCPServerConfig{
		{Name: "fs", Transport: "stdio", Command: "npx"},
	}}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-dh", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	found := false
	for _, s := range fake.execScripts {
		if strings.Contains(s, ".factory/") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no exec script targeted .factory/ MCP config; got %v", fake.execScripts)
	}
}

// =============================================================================
// cursorAdapter
// =============================================================================

func TestCursorAdapter_SetupSystemPrompt_HappyPath(t *testing.T) {
	fake := &adapterTestContainer{}
	err := cursorAdapter{}.SetupSystemPrompt(
		context.Background(), fake, "ct-c", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Fatalf("SetupSystemPrompt: %v", err)
	}
	if fake.execCalls < 5 {
		t.Errorf("execCalls = %d, want ≥ 5", fake.execCalls)
	}
}

func TestCursorAdapter_SetupSystemPrompt_AllWritesFail_WrapsWithAdapterName(t *testing.T) {
	want := errors.New("exec rejected")
	fake := &adapterTestContainer{execErr: want}
	a := cursorAdapter{}
	err := a.SetupSystemPrompt(
		context.Background(), fake, "ct-cf", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
	if !strings.Contains(err.Error(), "cursor adapter setup system prompt") {
		t.Errorf("err = %v, want \"cursor adapter setup system prompt\" prefix", err)
	}
}

func TestCursorAdapter_SupportsMCP_False(t *testing.T) {
	// Source comment is explicit: Cursor's --print mode does NOT
	// honour MCP servers. Returning true would mislead paymaster +
	// Crow's Nest into believing tools fire when they don't. Pin
	// false until upstream forum #143045/#148397 are resolved.
	a := cursorAdapter{}
	if a.SupportsMCP() {
		t.Error("SupportsMCP() = true; should remain false until cursor-agent --print honours MCP")
	}
}

func TestCursorAdapter_WriteMCPConfig_EmptyMCP_ShortCircuitsToNil(t *testing.T) {
	// Even though SupportsMCP() == false (orchestrator gates the call
	// upstream), the writer is kept wired so flipping SupportsMCP to
	// true in the future is the only change required. Pin that the
	// nil-MCP fast-path returns nil.
	fake := &adapterTestContainer{}
	a := cursorAdapter{}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-cm", AgentRunRequest{}, "/work", quietAdapterLogger(),
	)
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if fake.execCalls != 0 {
		t.Errorf("empty MCP triggered %d Exec calls; expected 0", fake.execCalls)
	}
}

func TestCursorAdapter_WriteMCPConfig_WrapsFailure(t *testing.T) {
	want := errors.New("rejected")
	fake := &adapterTestContainer{execErr: want}
	a := cursorAdapter{}
	req := AgentRunRequest{MCPServers: []MCPServerConfig{
		{Name: "fs", Transport: "stdio", Command: "npx"},
	}}
	err := a.WriteMCPConfig(
		context.Background(), fake, "ct-cmf", req, "/work", quietAdapterLogger(),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "cursor adapter write MCP config") {
		t.Errorf("err = %v, want \"cursor adapter write MCP config\" prefix", err)
	}
}

func TestCursorAdapter_WriteMCPConfig_HappyPath_TargetsCursorMCP(t *testing.T) {
	// writeMCPCursor writes to .cursor/mcp.json — pin the per-CLI
	// file path so a refactor doesn't silently redirect MCP config.
	fake := &adapterTestContainer{}
	a := cursorAdapter{}
	req := AgentRunRequest{MCPServers: []MCPServerConfig{
		{Name: "fs", Transport: "stdio", Command: "npx"},
	}}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-ch", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	found := false
	for _, s := range fake.execScripts {
		if strings.Contains(s, ".cursor/mcp.json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no exec script targeted .cursor/mcp.json; got %v", fake.execScripts)
	}
}
