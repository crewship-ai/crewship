package orchestrator

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestMemoryMCPSpec_PointsAtSidecarLoopback locks the URL contract every
// in-container CLI sees for the memory MCP server. If this drifts from
// what the sidecar's handleMemoryMCP listens on (sidecar.DefaultAddr +
// /mcp/memory) every adapter silently 404s the model's first memory call.
func TestMemoryMCPSpec_PointsAtSidecarLoopback(t *testing.T) {
	spec := memoryMCPSpec("")
	if spec.Name != "crewship-memory" {
		t.Errorf("server name = %q, want crewship-memory", spec.Name)
	}
	if spec.URL == "" {
		t.Fatalf("spec.URL must be set; got empty")
	}
	if !strings.HasPrefix(spec.URL, "http://127.0.0.1:") {
		t.Errorf("spec.URL = %q, want http://127.0.0.1:<port>/...", spec.URL)
	}
	if !strings.HasSuffix(spec.URL, "/mcp/memory") {
		t.Errorf("spec.URL = %q, want suffix /mcp/memory", spec.URL)
	}
	if spec.Transport != "http" {
		t.Errorf("transport = %q, want http", spec.Transport)
	}
}

// TestInjectMemoryMCP_AddsCrewshipEntry_WhenAbsent verifies a fresh spec
// list (no MCP servers configured by the user) gets the memory server
// injected. This is the "default-on" guarantee: every agent run with
// MemoryEnabled true gets memory tools regardless of crew-level MCP config.
func TestInjectMemoryMCP_AddsCrewshipEntry_WhenAbsent(t *testing.T) {
	out := injectMemoryMCP(nil, "sam", true)
	if len(out) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(out))
	}
	if out[0].Name != "crewship-memory" {
		t.Errorf("name = %q, want crewship-memory", out[0].Name)
	}
}

// TestInjectMemoryMCP_DoesNotOverrideUserEntry — defensive contract.
// If a user explicitly defined an MCP server with name "crewship-memory"
// (unlikely, but namespace is open), we leave their entry intact rather
// than clobbering it. Surfacing a clobber would silently break the user's
// expectations and we have no way to know what their server does.
func TestInjectMemoryMCP_DoesNotOverrideUserEntry(t *testing.T) {
	user := []mcpSpec{{Name: "crewship-memory", URL: "http://user.example/mcp"}}
	out := injectMemoryMCP(user, "sam", true)
	if len(out) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(out))
	}
	if out[0].URL != "http://user.example/mcp" {
		t.Errorf("URL = %q, want user's value preserved", out[0].URL)
	}
}

// TestInjectMemoryMCP_KeepsOtherEntries verifies we append (not replace)
// when other MCP servers are configured — the memory server lives
// alongside Linear, GitHub, etc.
func TestInjectMemoryMCP_KeepsOtherEntries(t *testing.T) {
	in := []mcpSpec{{Name: "linear", URL: "https://linear.example/mcp"}}
	out := injectMemoryMCP(in, "sam", true)
	if len(out) != 2 {
		t.Fatalf("expected 2 specs, got %d", len(out))
	}
	names := map[string]bool{}
	for _, s := range out {
		names[s.Name] = true
	}
	if !names["linear"] || !names["crewship-memory"] {
		t.Errorf("missing expected entries; got=%v", names)
	}
}

// TestInjectMemoryMCPIntoClaudeJSON_SetsAlwaysLoad is the load-bearing
// assertion for eager tool loading. Claude Code DEFERS MCP tools by default —
// the model must spend a ToolSearch round-trip to discover memory.read/write/
// search/append_daily before it can call them, even though it needs them
// almost every turn. Marking our injected first-party server with
// "alwaysLoad": true makes Claude Code present those schemas eagerly at session
// start (no discovery hop). Claude-only .mcp.json field (v2.1.121+).
func TestInjectMemoryMCPIntoClaudeJSON_SetsAlwaysLoad(t *testing.T) {
	out, err := injectMemoryMCPIntoClaudeJSON(`{"mcpServers":{}}`, "sam", true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "crewship-memory") {
		t.Fatalf("output missing crewship-memory server: %s", out)
	}
	if !strings.Contains(out, `"alwaysLoad":true`) {
		t.Errorf("injected memory server missing alwaysLoad:true — tools stay deferred behind ToolSearch: %s", out)
	}
}

// TestInjectMemoryMCPIntoClaudeJSON_PreservesUserEntry — a user-declared server
// under our reserved name is left exactly as-is; we do NOT force alwaysLoad
// onto an operator's own entry.
func TestInjectMemoryMCPIntoClaudeJSON_PreservesUserEntry(t *testing.T) {
	in := `{"mcpServers":{"crewship-memory":{"type":"http","url":"http://user/mcp"}}}`
	out, err := injectMemoryMCPIntoClaudeJSON(in, "sam", true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "http://user/mcp") {
		t.Errorf("user entry not preserved: %s", out)
	}
	if strings.Contains(out, "alwaysLoad") {
		t.Errorf("must not inject alwaysLoad onto a user-declared entry: %s", out)
	}
}

// TestMemoryMCPSpec_PerAgentPath pins the CRE-137 fix: a non-empty
// agent slug scopes the injected URL to /mcp/memory/<slug>, so every
// crew member's memory calls resolve to its OWN tier instead of the
// tier of whichever agent started the shared sidecar.
func TestMemoryMCPSpec_PerAgentPath(t *testing.T) {
	spec := memoryMCPSpec("sam")
	if !strings.HasSuffix(spec.URL, "/mcp/memory/sam") {
		t.Errorf("spec.URL = %q, want suffix /mcp/memory/sam", spec.URL)
	}
}

func TestInjectMemoryMCPIntoClaudeJSON_PerAgentURL(t *testing.T) {
	out, err := injectMemoryMCPIntoClaudeJSON(`{"mcpServers":{}}`, "alex", true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "/mcp/memory/alex") {
		t.Errorf("injected URL must carry the agent slug; got %s", out)
	}
}

// TestInjectMemoryMCP_HealthGated is the 2b guarantee (health-gated
// capability advertisement — Circuit Breaker / "don't advertise a
// backend that's down"). When the memory sink is not reachable the
// tool MUST NOT be advertised to the model, so a SkipSidecar worker in
// a genuinely cold container degrades explicitly (no memory tool)
// instead of getting a false ACK from a dead :9119.
func TestInjectMemoryMCP_HealthGated(t *testing.T) {
	hasMem := func(specs []mcpSpec) bool {
		for _, s := range specs {
			if s.Name == MemoryMCPServerName {
				return true
			}
		}
		return false
	}

	t.Run("sink ready -> advertised", func(t *testing.T) {
		out := injectMemoryMCP(nil, "riley", true)
		if !hasMem(out) {
			t.Fatal("memory tool must be advertised when the sink is ready")
		}
	})

	t.Run("sink down -> not advertised", func(t *testing.T) {
		out := injectMemoryMCP(nil, "riley", false)
		if hasMem(out) {
			t.Fatal("memory tool must NOT be advertised when the sink is down (silent-loss class)")
		}
	})
}

// TestInjectMemoryMCPIntoClaudeJSON_HealthGated is the Claude .mcp.json
// twin of the 2b guarantee.
func TestInjectMemoryMCPIntoClaudeJSON_HealthGated(t *testing.T) {
	const empty = `{"mcpServers":{}}`

	ready, err := injectMemoryMCPIntoClaudeJSON(empty, "riley", true)
	if err != nil {
		t.Fatalf("ready: unexpected error: %v", err)
	}
	if !strings.Contains(ready, MemoryMCPServerName) {
		t.Fatal("memory tool must be present in Claude config when the sink is ready")
	}

	down, err := injectMemoryMCPIntoClaudeJSON(empty, "riley", false)
	if err != nil {
		t.Fatalf("down: unexpected error: %v", err)
	}
	if strings.Contains(down, MemoryMCPServerName) {
		t.Fatal("memory tool must be absent from Claude config when the sink is down")
	}
}

// TestMemoryMCPSpec_CarriesAgentTokenHeader — #812: the injected memory MCP
// server presents the per-agent bearer token so the sidecar resolves the
// ACTING agent from authentication, not the caller-supplied URL slug.
func TestMemoryMCPSpec_CarriesAgentTokenHeader(t *testing.T) {
	spec := memoryMCPSpec("sam")
	got := spec.Headers["Authorization"]
	if got != "Bearer ${CREWSHIP_AGENT_TOKEN}" {
		t.Errorf("Authorization header = %q, want Bearer ${CREWSHIP_AGENT_TOKEN}", got)
	}
}

func TestInjectMemoryMCPIntoClaudeJSON_CarriesAgentTokenHeader(t *testing.T) {
	out, err := injectMemoryMCPIntoClaudeJSON(`{"mcpServers":{}}`, "alex", true)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, `Bearer ${CREWSHIP_AGENT_TOKEN}`) {
		t.Errorf("Claude MCP entry missing per-agent auth header: %s", out)
	}
}

// TestAgentAuthToken_DerivesPerAgent — the orchestrator helper mints a
// distinct, deterministic token per (workspace, agent) and fails closed on
// empty inputs.
func TestAgentAuthToken_DerivesPerAgent(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	a := agentAuthToken("master", "ws-1", "agent-a", log)
	b := agentAuthToken("master", "ws-1", "agent-b", log)
	if a == "" || b == "" || a == b {
		t.Fatalf("expected distinct non-empty tokens, got a=%q b=%q", a, b)
	}
	if agentAuthToken("", "ws-1", "agent-a", log) != "" {
		t.Error("empty master must yield empty token (fail closed)")
	}
	if agentAuthToken("master", "", "agent-a", log) != "" {
		t.Error("empty workspace must yield empty token (fail closed)")
	}
	if agentAuthToken("master", "ws-1", "", log) != "" {
		t.Error("empty agent id must yield empty token (fail closed)")
	}
}
