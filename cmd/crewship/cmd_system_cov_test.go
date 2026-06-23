package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── system info ─────────────────────────────────────────────────────────

func TestSystemInfoRunE_HappyPath(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "runtime": "docker", "version": "27.0", "socket": "/var/run/docker.sock",
	}))
	stub.OnGet("/api/v1/system/license", clitest.JSONResponse(200, map[string]any{
		"edition": "FREE", "max_agents_per_crew": 5, "max_crews": 3, "max_members": 10,
		"licensee_org": "Acme",
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"docker", "27.0", "/var/run/docker.sock", "FREE", "Acme", "Max crews:        3"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestSystemInfoRunE_LicenseUnavailable(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": false, "runtime": "none", "version": "",
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "no license endpoint"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("license 404 must not fail the command: %v", err)
	}
	if strings.Contains(out, "License") {
		t.Errorf("license section should be absent on non-200; got:\n%s", out)
	}
}

func TestSystemInfoRunE_RuntimeError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.ErrorResponse(500, "wedged"))

	err := systemInfoCmd.RunE(systemInfoCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "wedged") {
		t.Errorf("expected runtime error to bubble; got %v", err)
	}
}

func TestSystemInfoRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := systemInfoCmd.RunE(systemInfoCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// ─── system keeper ───────────────────────────────────────────────────────

func TestSystemKeeperRunE_Enabled(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/keeper", clitest.JSONResponse(200, map[string]any{
		"enabled": true, "ollama_url": "http://localhost:11434", "model": "phi3:mini",
		"ollama_online": true, "secret_count": 4,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemKeeperCmd.RunE(systemKeeperCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"enabled", "online", "phi3:mini", "Secret creds: 4", "http://localhost:11434"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestSystemKeeperRunE_Disabled(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/keeper", clitest.JSONResponse(200, map[string]any{
		"enabled": false, "ollama_online": false,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemKeeperCmd.RunE(systemKeeperCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "disabled") || !strings.Contains(out, "offline") {
		t.Errorf("expected disabled/offline; got:\n%s", out)
	}
}

func TestSystemKeeperRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/keeper", clitest.ErrorResponse(503, "keeper down"))

	err := systemKeeperCmd.RunE(systemKeeperCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "keeper down") {
		t.Errorf("expected API error; got %v", err)
	}
}

// ─── system stats ────────────────────────────────────────────────────────

func TestSystemStatsRunE_Table(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/admin/stats", clitest.JSONResponse(200, map[string]any{
		"workspaces": 2, "users": 5, "agents": 9, "running": 1,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemStatsCmd.RunE(systemStatsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Workspaces: 2", "Users:      5", "Agents:     9", "Running:    1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
}

func TestSystemStatsRunE_JSON(t *testing.T) {
	stub := covSetupCli5(t)
	flagFormat = "json"
	stub.OnGet("/api/v1/admin/stats", clitest.JSONResponse(200, map[string]any{
		"workspaces": 1, "users": 1, "agents": 1, "running": 0,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemStatsCmd.RunE(systemStatsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var v struct {
		Workspaces int `json:"workspaces"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &v); jsonErr != nil {
		t.Fatalf("output is not JSON: %v\n%s", jsonErr, out)
	}
	if v.Workspaces != 1 {
		t.Errorf("workspaces = %d, want 1", v.Workspaces)
	}
}

func TestSystemStatsRunE_NoWorkspace(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}

	err := systemStatsCmd.RunE(systemStatsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// ─── onboarding ──────────────────────────────────────────────────────────

func TestSystemOnboardingStatusRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/onboarding/status", clitest.JSONResponse(200, map[string]any{
		"completed": true,
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() {
		err = systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"completed": true`) {
		t.Errorf("expected JSON status; got:\n%s", out)
	}
}

func TestSystemOnboardingBareDelegatesToStatus(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/onboarding/status", clitest.JSONResponse(200, map[string]any{
		"completed": false,
	}))

	var err error
	covCaptureStdoutCli5(t, func() { err = systemOnboardingCmd.RunE(systemOnboardingCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("GET", "/api/v1/onboarding/status"); len(calls) != 1 {
		t.Errorf("bare onboarding should call status exactly once, got %d", len(calls))
	}
}

func TestSystemOnboardingSetupRunE_MissingFlags(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, systemOnboardingSetupCmd, "crew", "")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "agent", "")

	err := systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew and --agent are required") {
		t.Errorf("expected required-flags error; got %v", err)
	}
}

func TestSystemOnboardingSetupRunE_DeprecatedCredentialFlag(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, systemOnboardingSetupCmd, "crew", "backend")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "agent", "viktor")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "cli-adapter", "CLAUDE_CODE")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "llm-provider", "ANTHROPIC")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "llm-model", "claude-x")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "credential-name", "main key")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "credential-value", "sk-test-123")

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureStdoutCli5(t, func() { err = systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST setup, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	want := map[string]string{
		"crew_name":        "backend",
		"agent_name":       "viktor",
		"cli_adapter":      "CLAUDE_CODE",
		"llm_provider":     "ANTHROPIC",
		"llm_model":        "claude-x",
		"credential_name":  "main key",
		"credential_value": "sk-test-123",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %q, want %q", k, body[k], v)
		}
	}
}

func TestSystemOnboardingSetupRunE_CredentialFromStdin(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, systemOnboardingSetupCmd, "crew", "ops")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "agent", "eva")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "credential-value-stdin", "true")
	covSwapStdin(t, "sk-from-stdin\n")

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureStdoutCli5(t, func() { err = systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST setup, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["credential_value"] != "sk-from-stdin" {
		t.Errorf("credential_value = %q, want sk-from-stdin", body["credential_value"])
	}
}

func TestSystemOnboardingSetupRunE_EmptyStdin(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, systemOnboardingSetupCmd, "crew", "ops")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "agent", "eva")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "credential-value-stdin", "true")
	covSwapStdin(t, "")

	err := systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no input provided on stdin") {
		t.Errorf("expected empty-stdin error; got %v", err)
	}
}

func TestSystemOnboardingCompleteRunE(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/onboarding/complete", clitest.JSONResponse(200, map[string]any{"ok": true}))

	var err error
	covCaptureStdoutCli5(t, func() {
		err = systemOnboardingCompleteCmd.RunE(systemOnboardingCompleteCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", "/api/v1/onboarding/complete"); len(calls) != 1 {
		t.Errorf("expected 1 POST complete, got %d", len(calls))
	}
}

func TestSystemOnboardingCompleteRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnPost("/api/v1/onboarding/complete", clitest.ErrorResponse(500, "flip failed"))

	err := systemOnboardingCompleteCmd.RunE(systemOnboardingCompleteCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "flip failed") {
		t.Errorf("expected API error; got %v", err)
	}
}

// ─── error + format branches round 2 ─────────────────────────────────────

func TestSystemAuxStatusRunE_MillisecondTimeout(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/aux-status", clitest.JSONResponse(200, map[string]any{
		"slots": []map[string]any{
			{"slot": "keeper", "provider": "ollama", "model": "phi3", "timeout_ms": 500, "source": "explicit"},
		},
	}))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "500ms") {
		t.Errorf("sub-second timeout must render in ms; got:\n%s", out)
	}
}

func TestSystemAuxStatusRunE_MalformedResponse(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/aux-status", clitest.TextResponse(200, "not json"))

	err := systemAuxStatusCmd.RunE(systemAuxStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemInfoRunE_MalformedRuntime(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.TextResponse(200, "not json"))

	err := systemInfoCmd.RunE(systemInfoCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemKeeperRunE_NoAuthAndMalformed(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := systemKeeperCmd.RunE(systemKeeperCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/keeper", clitest.TextResponse(200, "not json"))
	err = systemKeeperCmd.RunE(systemKeeperCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemStatsRunE_ErrorBranches(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := systemStatsCmd.RunE(systemStatsCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/admin/stats", clitest.ErrorResponse(403, "Forbidden"))
	if err := systemStatsCmd.RunE(systemStatsCmd, nil); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("expected 403; got %v", err)
	}

	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/admin/stats", clitest.TextResponse(200, "not json"))
	if err := systemStatsCmd.RunE(systemStatsCmd, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemOnboardingStatusRunE_ErrorBranches(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/onboarding/status", clitest.ErrorResponse(500, "status wedged"))
	if err := systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "status wedged") {
		t.Errorf("expected 500; got %v", err)
	}

	stub2 := covSetupCli5(t)
	stub2.OnGet("/api/v1/onboarding/status", clitest.TextResponse(200, "not json"))
	if err := systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemOnboardingSetupRunE_ErrorBranches(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	if err := systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	stub := covSetupCli5(t)
	covSetFlagCli5(t, systemOnboardingSetupCmd, "crew", "c")
	covSetFlagCli5(t, systemOnboardingSetupCmd, "agent", "a")
	stub.OnPost("/api/v1/onboarding/setup", clitest.ErrorResponse(422, "agent name taken"))
	var err error
	covCaptureAll(t, func() { err = systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "agent name taken") {
		t.Errorf("expected 422; got %v", err)
	}

	stub.OnPost("/api/v1/onboarding/setup", clitest.TextResponse(200, "not json"))
	covCaptureAll(t, func() { err = systemOnboardingSetupCmd.RunE(systemOnboardingSetupCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestSystemOnboardingCompleteRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := systemOnboardingCompleteCmd.RunE(systemOnboardingCompleteCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}
