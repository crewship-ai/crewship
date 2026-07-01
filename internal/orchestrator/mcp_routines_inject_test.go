package orchestrator

import (
	"strings"
	"testing"
)

// TestRoutinesMCPSpec_PointsAtSidecarLoopback locks the URL contract every
// in-container CLI sees for the routine-authoring MCP server. If this drifts
// from what the sidecar's handleRoutinesMCP listens on (DefaultAddr +
// /mcp/routines) every adapter silently 404s the model's first save_routine.
func TestRoutinesMCPSpec_PointsAtSidecarLoopback(t *testing.T) {
	spec := routinesMCPSpec()
	if spec.Name != "crewship-routines" {
		t.Errorf("server name = %q, want crewship-routines", spec.Name)
	}
	if !strings.HasPrefix(spec.URL, "http://127.0.0.1:") {
		t.Errorf("spec.URL = %q, want http://127.0.0.1:<port>/...", spec.URL)
	}
	if !strings.HasSuffix(spec.URL, "/mcp/routines") {
		t.Errorf("spec.URL = %q, want suffix /mcp/routines", spec.URL)
	}
	if spec.Transport != "http" {
		t.Errorf("transport = %q, want http", spec.Transport)
	}
}

// TestInjectRoutinesMCP_AddsEntry_WhenAbsent — default-on guarantee.
func TestInjectRoutinesMCP_AddsEntry_WhenAbsent(t *testing.T) {
	out := injectRoutinesMCP(nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(out))
	}
	if out[0].Name != "crewship-routines" {
		t.Errorf("name = %q, want crewship-routines", out[0].Name)
	}
}

// TestInjectRoutinesMCP_DoesNotOverrideUserEntry — user override wins.
func TestInjectRoutinesMCP_DoesNotOverrideUserEntry(t *testing.T) {
	user := []mcpSpec{{Name: "crewship-routines", URL: "http://user.example/mcp"}}
	out := injectRoutinesMCP(user)
	if len(out) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(out))
	}
	if out[0].URL != "http://user.example/mcp" {
		t.Errorf("URL = %q, want user's value preserved", out[0].URL)
	}
}

// TestInjectRoutinesMCP_CoexistsWithMemory verifies both servers land when
// both injectors run (the real adapter path) — distinct names, both present.
func TestInjectRoutinesMCP_CoexistsWithMemory(t *testing.T) {
	specs := injectMemoryMCP(nil)
	specs = injectRoutinesMCP(specs)
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
	}
	if !names["crewship-memory"] || !names["crewship-routines"] {
		t.Fatalf("expected both servers, got %v", names)
	}
}

// TestInjectRoutinesMCPIntoClaudeJSON_AddsServer mirrors the memory JSON
// injector: a fresh {"mcpServers":{}} gains the crewship-routines entry.
func TestInjectRoutinesMCPIntoClaudeJSON_AddsServer(t *testing.T) {
	out, err := injectRoutinesMCPIntoClaudeJSON(`{"mcpServers":{}}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "crewship-routines") || !strings.Contains(out, "/mcp/routines") {
		t.Errorf("output missing routines server: %s", out)
	}
}

// TestInjectRoutinesMCPIntoClaudeJSON_SetsAlwaysLoad asserts the injected
// first-party server is flagged alwaysLoad so Claude Code presents save_routine/
// list_routines/run_routine EAGERLY at session start — no ToolSearch discovery
// round-trip before the agent can author or run a routine. Claude-only
// .mcp.json field (v2.1.121+); the other CLIs load MCP tools eagerly already.
func TestInjectRoutinesMCPIntoClaudeJSON_SetsAlwaysLoad(t *testing.T) {
	out, err := injectRoutinesMCPIntoClaudeJSON(`{"mcpServers":{}}`)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, `"alwaysLoad":true`) {
		t.Errorf("injected routines server missing alwaysLoad:true — tools stay deferred behind ToolSearch: %s", out)
	}
}

// TestInjectRoutinesMCPIntoClaudeJSON_PreservesUserEntry — override wins, and
// we do NOT force alwaysLoad onto an operator's own entry.
func TestInjectRoutinesMCPIntoClaudeJSON_PreservesUserEntry(t *testing.T) {
	in := `{"mcpServers":{"crewship-routines":{"type":"http","url":"http://user/mcp"}}}`
	out, err := injectRoutinesMCPIntoClaudeJSON(in)
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

// TestRoutinesMCPServerName_MatchesSidecar guards the cross-package string
// contract: orchestrator advertises the same name the sidecar serves under.
func TestRoutinesMCPServerName_MatchesSidecar(t *testing.T) {
	if RoutinesMCPServerName != "crewship-routines" {
		t.Errorf("RoutinesMCPServerName = %q; sidecar.RoutinesMCPServerName is hard-coded to crewship-routines", RoutinesMCPServerName)
	}
}
