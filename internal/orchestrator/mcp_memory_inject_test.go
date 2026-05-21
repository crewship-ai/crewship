package orchestrator

import (
	"strings"
	"testing"
)

// TestMemoryMCPSpec_PointsAtSidecarLoopback locks the URL contract every
// in-container CLI sees for the memory MCP server. If this drifts from
// what the sidecar's handleMemoryMCP listens on (sidecar.DefaultAddr +
// /mcp/memory) every adapter silently 404s the model's first memory call.
func TestMemoryMCPSpec_PointsAtSidecarLoopback(t *testing.T) {
	spec := memoryMCPSpec()
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
	out := injectMemoryMCP(nil)
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
	out := injectMemoryMCP(user)
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
	out := injectMemoryMCP(in)
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
