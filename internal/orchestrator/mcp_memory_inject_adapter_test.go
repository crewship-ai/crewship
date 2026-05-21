package orchestrator

import (
	"context"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PR-A F1: per-adapter wiring tests for the memory MCP server injection.
//
// Each MCP-capable adapter (Claude / Codex / Gemini / OpenCode / Droid)
// MUST auto-inject the crewship-memory MCP server pointing at the sidecar
// /mcp/memory loopback regardless of whether the crew/agent declared any
// other MCP servers. This is the wire-level guarantee that satisfies F1:
// "every supported CLI gets native memory.* tool calls without per-deploy
// configuration".
//
// Cursor is intentionally NOT tested here — adapter_cursor.go
// SupportsMCP() returns false because cursor-agent's --print mode ignores
// .cursor/mcp.json. Wiring memory MCP into Cursor would mislead operators
// into thinking memory works there when it does not. See
// adapter_cursor.go SupportsMCP comment + the deferral note in the PR.
// ---------------------------------------------------------------------------

// decodeBase64FromShellScript extracts the first `echo <b64> | base64 -d`
// payload from a writeFileViaContainer-generated shell script and returns
// the decoded bytes. Used by the per-adapter tests below to peek at the
// JSON / TOML content the adapter wrote without standing up a real
// container.
func decodeBase64FromShellScript(t *testing.T, script string) string {
	t.Helper()
	// writeFileViaContainer emits: `... echo <BASE64> | base64 -d > <path> ...`
	re := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d`)
	m := re.FindStringSubmatch(script)
	if m == nil {
		// setupMCPConfig uses single-quoted echo: `echo '<B64>' | base64 -d ...`
		re2 := regexp.MustCompile(`echo '([A-Za-z0-9+/=]+)' \| base64 -d`)
		m = re2.FindStringSubmatch(script)
	}
	if m == nil {
		t.Fatalf("no base64 payload found in script: %s", script)
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("decode base64 from script: %v", err)
	}
	return string(raw)
}

// findScriptForPath returns the first shell script that mentions the given
// path substring. Each adapter's writer drops one file per WriteMCPConfig
// call; the path substring identifies which CLI's config we're looking at.
func findScriptForPath(t *testing.T, scripts []string, substr string) string {
	t.Helper()
	for _, s := range scripts {
		if strings.Contains(s, substr) {
			return s
		}
	}
	t.Fatalf("no script targeted %q; got %v", substr, scripts)
	return ""
}

// ----------------------------------------------------------------------
// CLAUDE_CODE: .mcp.json (Anthropic schema) → mcpServers.crewship-memory.url
// ----------------------------------------------------------------------

func TestClaudeAdapter_WriteMCPConfig_InjectsMemoryMCPServer(t *testing.T) {
	fake := &adapterTestContainer{}
	a := claudeCodeAdapter{}
	// No user-declared MCP — memory must still appear.
	req := AgentRunRequest{AgentSlug: "alpha"}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-claude", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	script := findScriptForPath(t, fake.execScripts, ".mcp.json")
	body := decodeBase64FromShellScript(t, script)
	if !strings.Contains(body, `"crewship-memory"`) {
		t.Errorf("Claude .mcp.json missing crewship-memory entry; body=%s", body)
	}
	if !strings.Contains(body, "/mcp/memory") {
		t.Errorf("Claude .mcp.json missing /mcp/memory URL; body=%s", body)
	}
}

// ----------------------------------------------------------------------
// CODEX_CLI: .codex/config.toml → [mcp_servers.crewship-memory] url=...
// ----------------------------------------------------------------------

func TestCodexAdapter_WriteMCPConfig_InjectsMemoryMCPServer(t *testing.T) {
	fake := &adapterTestContainer{}
	a := codexAdapter{}
	req := AgentRunRequest{AgentSlug: "beta"}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-codex", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	script := findScriptForPath(t, fake.execScripts, ".codex/config.toml")
	body := decodeBase64FromShellScript(t, script)
	if !strings.Contains(body, "[mcp_servers.crewship-memory]") {
		t.Errorf("Codex config.toml missing [mcp_servers.crewship-memory]; body=%s", body)
	}
	if !strings.Contains(body, "/mcp/memory") {
		t.Errorf("Codex config.toml missing /mcp/memory URL; body=%s", body)
	}
}

// ----------------------------------------------------------------------
// GEMINI_CLI: .gemini/settings.json → mcpServers.crewship-memory.httpUrl
// ----------------------------------------------------------------------

func TestGeminiAdapter_WriteMCPConfig_InjectsMemoryMCPServer(t *testing.T) {
	fake := &adapterTestContainer{}
	a := geminiAdapter{}
	req := AgentRunRequest{AgentSlug: "gamma"}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-gemini", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	script := findScriptForPath(t, fake.execScripts, ".gemini/settings.json")
	body := decodeBase64FromShellScript(t, script)
	if !strings.Contains(body, `"crewship-memory"`) {
		t.Errorf("Gemini settings.json missing crewship-memory; body=%s", body)
	}
	if !strings.Contains(body, "/mcp/memory") {
		t.Errorf("Gemini settings.json missing /mcp/memory URL; body=%s", body)
	}
}

// ----------------------------------------------------------------------
// OPENCODE: opencode.json → mcp.crewship-memory.type=remote url=...
// ----------------------------------------------------------------------

func TestOpenCodeAdapter_WriteMCPConfig_InjectsMemoryMCPServer(t *testing.T) {
	fake := &adapterTestContainer{}
	a := opencodeAdapter{}
	req := AgentRunRequest{AgentSlug: "delta"}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-opencode", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	script := findScriptForPath(t, fake.execScripts, "opencode.json")
	body := decodeBase64FromShellScript(t, script)
	if !strings.Contains(body, `"crewship-memory"`) {
		t.Errorf("OpenCode opencode.json missing crewship-memory; body=%s", body)
	}
	if !strings.Contains(body, `"remote"`) {
		t.Errorf("OpenCode opencode.json missing type=remote; body=%s", body)
	}
	if !strings.Contains(body, "/mcp/memory") {
		t.Errorf("OpenCode opencode.json missing /mcp/memory URL; body=%s", body)
	}
}

// ----------------------------------------------------------------------
// FACTORY_DROID: .factory/mcp.json → mcpServers.crewship-memory.type=http
// ----------------------------------------------------------------------

func TestDroidAdapter_WriteMCPConfig_InjectsMemoryMCPServer(t *testing.T) {
	fake := &adapterTestContainer{}
	a := droidAdapter{}
	req := AgentRunRequest{AgentSlug: "epsilon"}
	if err := a.WriteMCPConfig(
		context.Background(), fake, "ct-droid", req, "/work", quietAdapterLogger(),
	); err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}
	script := findScriptForPath(t, fake.execScripts, ".factory/mcp.json")
	body := decodeBase64FromShellScript(t, script)
	if !strings.Contains(body, `"crewship-memory"`) {
		t.Errorf("Droid .factory/mcp.json missing crewship-memory; body=%s", body)
	}
	if !strings.Contains(body, `"http"`) {
		t.Errorf("Droid .factory/mcp.json missing type=http; body=%s", body)
	}
	if !strings.Contains(body, "/mcp/memory") {
		t.Errorf("Droid .factory/mcp.json missing /mcp/memory URL; body=%s", body)
	}
}

// ----------------------------------------------------------------------
// CURSOR_CLI: NOT tested for memory MCP — see file header. Verify the
// adapter still does not advertise MCP capability so this deferral is
// load-bearing in the rest of the system.
// ----------------------------------------------------------------------

func TestCursorAdapter_StillNoMCP_MemoryDeferred(t *testing.T) {
	a := cursorAdapter{}
	if a.SupportsMCP() {
		t.Fatal("Cursor adapter now advertises MCP — re-evaluate memory MCP wiring deferral and add a TestCursorAdapter_WriteMCPConfig_InjectsMemoryMCPServer test")
	}
}
