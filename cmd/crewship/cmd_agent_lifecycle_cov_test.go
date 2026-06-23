package main

// Coverage tests for cmd_agent_lifecycle.go (agent create / update /
// delete). Serial — they mutate the shared cliCfg + cobra flag globals.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── agent create ────────────────────────────────────────────────────────

func TestAgentCreateRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := agentCreateCmd.RunE(agentCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestAgentCreateRunE_RequiresName(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, agentCreateCmd, "name")

	err := agentCreateCmd.RunE(agentCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected '--name is required', got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("no HTTP call expected, got %d", n)
	}
}

func TestAgentCreateRunE_FullBody(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentCreateCmd, "name", "Eva")
	covSetFlagCli6(t, agentCreateCmd, "slug", "eva")
	covSetFlagCli6(t, agentCreateCmd, "role", "LEAD")
	covSetFlagCli6(t, agentCreateCmd, "role-title", "QA Lead")
	covSetFlagCli6(t, agentCreateCmd, "cli-adapter", "CLAUDE_CODE")
	covSetFlagCli6(t, agentCreateCmd, "tool-profile", "FULL")
	covSetFlagCli6(t, agentCreateCmd, "lead-mode", "active")
	covSetFlagCli6(t, agentCreateCmd, "llm-provider", "ANTHROPIC")
	covSetFlagCli6(t, agentCreateCmd, "llm-model", "test-model")
	covSetFlagCli6(t, agentCreateCmd, "timeout", "120")
	covSetFlagCli6(t, agentCreateCmd, "memory", "true")
	covSetFlagCli6(t, agentCreateCmd, "avatar-seed", "seed1")
	covSetFlagCli6(t, agentCreateCmd, "avatar-style", "pixel-art")
	covSetFlagCli6(t, agentCreateCmd, "system-prompt", "be nice")
	covSetFlagCli6(t, agentCreateCmd, "crew", "engineering")

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli6, "slug": "engineering"},
	}))
	stub.OnPost("/api/v1/agents", clitest.JSONResponse(201, map[string]string{
		"id": covAgentIDCli6, "slug": "eva", "name": "Eva",
	}))

	if err := agentCreateCmd.RunE(agentCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/agents")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST /api/v1/agents, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	want := map[string]any{
		"name":            "Eva",
		"slug":            "eva",
		"agent_role":      "LEAD",
		"role_title":      "QA Lead",
		"cli_adapter":     "CLAUDE_CODE",
		"tool_profile":    "FULL",
		"lead_mode":       "active",
		"llm_provider":    "ANTHROPIC",
		"llm_model":       "test-model",
		"timeout_seconds": float64(120),
		"memory_enabled":  true,
		"avatar_seed":     "seed1",
		"avatar_style":    "pixel-art",
		"system_prompt":   "be nice",
		"crew_id":         covCrewIDCli6,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
}

func TestAgentCreateRunE_SystemPromptFromFile(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("file prompt content"), 0o644); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli6(t, agentCreateCmd, "name", "Eva")
	covSetFlagCli6(t, agentCreateCmd, "system-prompt", "@"+promptFile)

	stub.OnPost("/api/v1/agents", clitest.JSONResponse(201, map[string]string{
		"id": covAgentIDCli6, "slug": "eva",
	}))

	if err := agentCreateCmd.RunE(agentCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covDecodeBody(t, stub.CallsFor("POST", "/api/v1/agents")[0].Body)
	if body["system_prompt"] != "file prompt content" {
		t.Errorf("system_prompt = %v, want file content", body["system_prompt"])
	}
}

func TestAgentCreateRunE_SystemPromptFileMissing(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentCreateCmd, "name", "Eva")
	covSetFlagCli6(t, agentCreateCmd, "system-prompt", "@"+filepath.Join(t.TempDir(), "nope.txt"))

	err := agentCreateCmd.RunE(agentCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "read system prompt file") {
		t.Errorf("expected read error, got %v", err)
	}
}

func TestAgentCreateRunE_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentCreateCmd, "name", "Eva")
	covSetFlagCli6(t, agentCreateCmd, "crew", "ghost")

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := agentCreateCmd.RunE(agentCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestAgentCreateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentCreateCmd, "name", "Eva")
	stub.OnPost("/api/v1/agents", clitest.ErrorResponse(422, "slug taken"))

	err := agentCreateCmd.RunE(agentCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "slug taken") {
		t.Errorf("expected 422 surfaced, got %v", err)
	}
}

// ─── agent update ────────────────────────────────────────────────────────

func TestAgentUpdateRunE_NoFields(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, agentUpdateCmd,
		"name", "role", "role-title", "cli-adapter", "tool-profile", "lead-mode",
		"llm-provider", "llm-model", "timeout", "memory", "system-prompt",
		"avatar-seed", "avatar-style")

	// CUID argument: resolveAgentID short-circuits, no HTTP call needed.
	err := agentUpdateCmd.RunE(agentUpdateCmd, []string{covAgentIDCli6})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update', got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("no HTTP call expected, got %d", n)
	}
}

func TestAgentUpdateRunE_PatchesChangedFields(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentUpdateCmd, "name", "Eva 2")
	covSetFlagCli6(t, agentUpdateCmd, "role", "AGENT")
	covSetFlagCli6(t, agentUpdateCmd, "role-title", "Engineer")
	covSetFlagCli6(t, agentUpdateCmd, "cli-adapter", "CODEX_CLI")
	covSetFlagCli6(t, agentUpdateCmd, "tool-profile", "MINIMAL")
	covSetFlagCli6(t, agentUpdateCmd, "lead-mode", "passive")
	covSetFlagCli6(t, agentUpdateCmd, "llm-provider", "OPENAI")
	covSetFlagCli6(t, agentUpdateCmd, "llm-model", "m2")
	covSetFlagCli6(t, agentUpdateCmd, "timeout", "60")
	covSetFlagCli6(t, agentUpdateCmd, "memory", "false")
	covSetFlagCli6(t, agentUpdateCmd, "system-prompt", "updated prompt")
	covSetFlagCli6(t, agentUpdateCmd, "avatar-seed", "s2")
	covSetFlagCli6(t, agentUpdateCmd, "avatar-style", "micah")

	stub.OnPatch("/api/v1/agents/"+covAgentIDCli6, clitest.EmptyResponse(200))

	if err := agentUpdateCmd.RunE(agentUpdateCmd, []string{covAgentIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/agents/"+covAgentIDCli6)
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	want := map[string]any{
		"name":            "Eva 2",
		"agent_role":      "AGENT",
		"role_title":      "Engineer",
		"cli_adapter":     "CODEX_CLI",
		"tool_profile":    "MINIMAL",
		"lead_mode":       "passive",
		"llm_provider":    "OPENAI",
		"llm_model":       "m2",
		"timeout_seconds": float64(60),
		"memory_enabled":  false,
		"system_prompt":   "updated prompt",
		"avatar_seed":     "s2",
		"avatar_style":    "micah",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
}

func TestAgentUpdateRunE_SystemPromptFromFile(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	promptFile := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(promptFile, []byte("from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli6(t, agentUpdateCmd, "system-prompt", "@"+promptFile)

	stub.OnPatch("/api/v1/agents/"+covAgentIDCli6, clitest.EmptyResponse(200))

	if err := agentUpdateCmd.RunE(agentUpdateCmd, []string{covAgentIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := covDecodeBody(t, stub.CallsFor("PATCH", "/api/v1/agents/"+covAgentIDCli6)[0].Body)
	if body["system_prompt"] != "from disk" {
		t.Errorf("system_prompt = %v, want file content", body["system_prompt"])
	}
}

func TestAgentUpdateRunE_SystemPromptFileMissing(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentUpdateCmd, "system-prompt", "@/nonexistent/prompt.txt")

	err := agentUpdateCmd.RunE(agentUpdateCmd, []string{covAgentIDCli6})
	if err == nil || !strings.Contains(err.Error(), "read system prompt file") {
		t.Errorf("expected read error, got %v", err)
	}
}

func TestAgentUpdateRunE_ResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentUpdateCmd, "name", "x")
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := agentUpdateCmd.RunE(agentUpdateCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
		t.Errorf("expected agent-not-found, got %v", err)
	}
}

func TestAgentUpdateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	covSetFlagCli6(t, agentUpdateCmd, "name", "x")
	stub.OnPatch("/api/v1/agents/"+covAgentIDCli6, clitest.ErrorResponse(403, "forbidden"))

	err := agentUpdateCmd.RunE(agentUpdateCmd, []string{covAgentIDCli6})
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

// ─── agent delete ────────────────────────────────────────────────────────

func TestAgentDeleteRunE_AbortedWithoutYes(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, agentDeleteCmd, "yes")

	// Non-TTY stdin: confirmAction falls back to Scanln which reads EOF
	// immediately → "aborted".
	err := agentDeleteCmd.RunE(agentDeleteCmd, []string{covAgentIDCli6})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected aborted, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("aborted delete must not issue HTTP calls, got %d", n)
	}
}

func TestAgentDeleteRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, agentDeleteCmd, "yes", "true")

	stub.OnDelete("/api/v1/agents/"+covAgentIDCli6, clitest.EmptyResponse(204))

	if err := agentDeleteCmd.RunE(agentDeleteCmd, []string{covAgentIDCli6}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/agents/"+covAgentIDCli6)); n != 1 {
		t.Errorf("expected exactly 1 DELETE, got %d", n)
	}
}

func TestAgentDeleteRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, agentDeleteCmd, "yes", "true")

	stub.OnDelete("/api/v1/agents/"+covAgentIDCli6, clitest.ErrorResponse(404, "agent gone"))

	err := agentDeleteCmd.RunE(agentDeleteCmd, []string{covAgentIDCli6})
	if err == nil || !strings.Contains(err.Error(), "agent gone") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}
