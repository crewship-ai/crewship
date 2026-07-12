package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// These tests lock the --format contract for the `system` family: a machine
// format must produce machine-parseable stdout. Historically `system info`
// and `system keeper` ignored --format entirely (ANSI human text under
// --format json broke any agent parsing stdout), `system stats` honored only
// json, and `onboarding status` emitted JSON even under --format yaml.

func TestSystemInfoRunE_FormatJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "runtime": "docker", "version": "27.0", "socket": "/var/run/docker.sock",
	}))
	stub.OnGet("/api/v1/system/license", clitest.JSONResponse(200, map[string]any{
		"edition": "FREE", "max_agents_per_crew": 5, "max_crews": 3, "max_members": 10,
	}))
	flagFormat = "json"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var payload struct {
		Runtime struct {
			Available bool   `json:"available"`
			Runtime   string `json:"runtime"`
		} `json:"runtime"`
		License *struct {
			Edition string `json:"edition"`
		} `json:"license"`
	}
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if payload.Runtime.Runtime != "docker" || !payload.Runtime.Available {
		t.Errorf("runtime payload mismatch: %+v", payload.Runtime)
	}
	if payload.License == nil || payload.License.Edition != "FREE" {
		t.Errorf("license payload mismatch: %+v", payload.License)
	}
}

func TestSystemInfoRunE_FormatJSON_LicenseUnavailable(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": false, "runtime": "none", "version": "",
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "no license endpoint"))
	flagFormat = "json"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("license 404 must not fail the command: %v", err)
	}
	var payload map[string]any
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if _, ok := payload["license"]; ok {
		t.Errorf("license key should be omitted when unavailable; got:\n%s", out)
	}
}

func TestSystemKeeperRunE_FormatJSON(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/keeper", clitest.JSONResponse(200, map[string]any{
		"enabled": true, "ollama_url": "http://localhost:11434", "model": "phi3:mini",
		"ollama_online": true, "secret_count": 4,
	}))
	flagFormat = "json"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemKeeperCmd.RunE(systemKeeperCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var payload struct {
		Enabled     bool `json:"enabled"`
		SecretCount int  `json:"secret_count"`
	}
	if uerr := json.Unmarshal([]byte(out), &payload); uerr != nil {
		t.Fatalf("--format json stdout is not valid JSON: %v\ngot:\n%s", uerr, out)
	}
	if !payload.Enabled || payload.SecretCount != 4 {
		t.Errorf("keeper payload mismatch: %+v", payload)
	}
}

func TestSystemStatsRunE_FormatYAML(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/admin/stats", clitest.JSONResponse(200, map[string]any{
		"workspaces": 3, "users": 7, "agents": 12, "running": 2,
	}))
	flagFormat = "yaml"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemStatsCmd.RunE(systemStatsCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// YAML (not the human table, not JSON): bare `workspaces: 3` line.
	if !strings.Contains(out, "workspaces: 3") {
		t.Errorf("--format yaml must emit YAML; got:\n%s", out)
	}
}

func TestOnboardingStatusRunE_FormatYAML(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/onboarding/status", clitest.JSONResponse(200, map[string]any{
		"completed": true,
	}))
	flagFormat = "yaml"

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// YAML renders the bool unquoted key: value; JSON would be `"completed": true`.
	if !strings.Contains(out, "completed: true") {
		t.Errorf("--format yaml must emit YAML; got:\n%s", out)
	}
}

// TestSystemInfoRunE_DefaultStaysHuman guards the flip side: without a machine
// format the hand-crafted human sections must stay byte-compatible (scripts
// and eyeballs alike rely on them).
func TestSystemInfoRunE_DefaultStaysHuman(t *testing.T) {
	stub := covSetupCli5(t)
	stub.OnGet("/api/v1/system/runtime", clitest.JSONResponse(200, map[string]any{
		"available": true, "runtime": "docker", "version": "27.0",
	}))
	stub.OnGet("/api/v1/system/license", clitest.ErrorResponse(404, "none"))

	var err error
	out := covCaptureStdoutCli5(t, func() { err = systemInfoCmd.RunE(systemInfoCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Container Runtime", "Runtime:    docker", "Version:    27.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output missing %q; got:\n%s", want, out)
		}
	}
}
