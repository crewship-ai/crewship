package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestBoolBadge(t *testing.T) {
	if got := boolBadge(true); got != "on" {
		t.Errorf("true: got %q want on", got)
	}
	if got := boolBadge(false); got != "off" {
		t.Errorf("false: got %q want off", got)
	}
}

func TestFeatureFlagListRunE_TableRendersOverride(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	override := true
	s.OnGet("/api/v1/feature-flags", clitest.JSONResponse(200, []featureFlagItem{
		{ID: "ff1", Key: "wake-gates", Enabled: false, Percentage: 100, OverrideEnabled: &override},
		{ID: "ff2", Key: "agentless", Enabled: true, Percentage: 50},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return featureFlagListCmd.RunE(featureFlagListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Overridden flag: default off, workspace on, effective on.
	if !strings.Contains(out, "wake-gates") {
		t.Errorf("output missing flag key: %q", out)
	}
	if !strings.Contains(out, "inherit") {
		t.Errorf("non-overridden flag should show inherit: %q", out)
	}
	if !strings.Contains(out, "100%") || !strings.Contains(out, "50%") {
		t.Errorf("percentages missing: %q", out)
	}
}

func TestFeatureFlagListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := featureFlagListCmd.RunE(featureFlagListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestFeatureFlagListRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/feature-flags", clitest.ErrorResponse(500, "flag store down"))
	covSetupCli10(t, s.URL())
	if err := featureFlagListCmd.RunE(featureFlagListCmd, nil); err == nil {
		t.Error("expected error from 500")
	}
}

func TestSetOverride_EnablePutsTrue(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/feature-flags/wake-gates/override", clitest.JSONResponse(200, map[string]bool{"enabled": true}))
	covSetupCli10(t, s.URL())

	if err := featureFlagEnableCmd.RunE(featureFlagEnableCmd, []string{"wake-gates"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PUT", "/api/v1/feature-flags/wake-gates/override")
	if len(calls) != 1 {
		t.Fatalf("PUT calls = %d, want 1", len(calls))
	}
	if !strings.Contains(string(calls[0].Body), `"enabled":true`) {
		t.Errorf("body = %s, want enabled:true", calls[0].Body)
	}
}

func TestSetOverride_DisablePutsFalse(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/feature-flags/agentless/override", clitest.JSONResponse(200, map[string]bool{"enabled": false}))
	covSetupCli10(t, s.URL())

	if err := featureFlagDisableCmd.RunE(featureFlagDisableCmd, []string{"agentless"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PUT", "/api/v1/feature-flags/agentless/override")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"enabled":false`) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestSetOverride_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := setOverride("k", true); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestSetOverride_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := setOverride("k", true); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestSetOverride_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/feature-flags/k/override", clitest.ErrorResponse(404, "unknown flag"))
	covSetupCli10(t, s.URL())
	err := setOverride("k", true)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected server error surfaced, got %v", err)
	}
}

func TestFeatureFlagInheritRunE_DeletesOverride(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/feature-flags/wake-gates/override", clitest.EmptyResponse(204))
	covSetupCli10(t, s.URL())

	if err := featureFlagInheritCmd.RunE(featureFlagInheritCmd, []string{"wake-gates"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/feature-flags/wake-gates/override")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestFeatureFlagInheritRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := featureFlagInheritCmd.RunE(featureFlagInheritCmd, []string{"k"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestFeatureFlagInheritRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/feature-flags/k/override", clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := featureFlagInheritCmd.RunE(featureFlagInheritCmd, []string{"k"}); err == nil {
		t.Error("expected error from 500")
	}
}
