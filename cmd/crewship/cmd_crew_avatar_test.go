package main

// Tests for `crewship crew apply-avatar-style` (issue #966 part 3 —
// API<->CLI parity: POST /api/v1/crews/{crewId}/apply-avatar-style had
// no CLI command). Drives the endpoint via the CLI RunE, matching the
// existing crew_provision cov-test harness (covSetupCli5 family).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCrewApplyAvatarStyleCmdStructure(t *testing.T) {
	if crewApplyAvatarStyleCmd.Use != "apply-avatar-style <slug-or-id>" {
		t.Errorf("Use = %q", crewApplyAvatarStyleCmd.Use)
	}
	for _, flag := range []string{"style", "reset"} {
		if crewApplyAvatarStyleCmd.Flags().Lookup(flag) == nil {
			t.Errorf("apply-avatar-style missing --%s flag", flag)
		}
	}
}

func TestCrewApplyAvatarStyleRunE_SetsStyle(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "style", "bottts-neutral")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/apply-avatar-style", clitest.JSONResponse(200, map[string]any{
		"updated": 3, "style": "bottts-neutral",
	}))

	out := covCaptureStdoutCli5(t, func() {
		if err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{covCrewIDCli5}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli5+"/apply-avatar-style")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST apply-avatar-style, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["avatar_style"] != "bottts-neutral" {
		t.Errorf("avatar_style = %v, want bottts-neutral", body["avatar_style"])
	}
	if _, has := body["reset_overrides"]; has {
		t.Errorf("reset_overrides should not be sent when setting a style: %v", body)
	}
	if !strings.Contains(out, "bottts-neutral") || !strings.Contains(out, "3") {
		t.Errorf("stdout should mention the style + updated count, got: %q", out)
	}
}

func TestCrewApplyAvatarStyleRunE_Reset(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "reset", "true")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/apply-avatar-style", clitest.JSONResponse(200, map[string]any{
		"updated": 3, "reset": true,
	}))

	out := covCaptureStdoutCli5(t, func() {
		if err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{covCrewIDCli5}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli5+"/apply-avatar-style")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST apply-avatar-style, got %d", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["reset_overrides"] != true {
		t.Errorf("reset_overrides = %v, want true", body["reset_overrides"])
	}
	if _, has := body["avatar_style"]; has {
		t.Errorf("avatar_style should not be sent on --reset: %v", body)
	}
	if !strings.Contains(strings.ToLower(out), "reset") {
		t.Errorf("stdout should mention the reset, got: %q", out)
	}
}

func TestCrewApplyAvatarStyleRunE_RequiresStyleOrReset(t *testing.T) {
	covSetupCli5(t)

	err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "--style") {
		t.Errorf("expected usage error mentioning --style; got %v", err)
	}
}

func TestCrewApplyAvatarStyleRunE_StyleAndResetMutuallyExclusive(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "style", "pixel-art")
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "reset", "true")

	err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error; got %v", err)
	}
}

func TestCrewApplyAvatarStyleRunE_UnknownCrew(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "style", "pixel-art")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{"ghost-crew"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestCrewApplyAvatarStyleRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, crewApplyAvatarStyleCmd, "style", "pixel-art")
	stub.OnPost("/api/v1/crews/"+covCrewIDCli5+"/apply-avatar-style", clitest.ErrorResponse(404, "Crew not found"))

	err := crewApplyAvatarStyleCmd.RunE(crewApplyAvatarStyleCmd, []string{covCrewIDCli5})
	if err == nil || !strings.Contains(err.Error(), "Crew not found") {
		t.Errorf("expected server 404 surfaced; got %v", err)
	}
}
