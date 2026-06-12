package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covBindingAgentID = "cagent7890abcdefghijklm"
	covBindingPath    = "/api/v1/agents/" + covBindingAgentID + "/integrations/bind-1"
)

func TestIntgAgentUpdateBindingRunE_NoFieldsToUpdate(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	err := intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected no-fields guard, got %v", err)
	}
}

func TestIntgAgentUpdateBindingRunE_EnabledPatch(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covBindingPath, clitest.JSONResponse(200, map[string]any{"id": "bind-1"}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "enabled", "false")

	out, err := captureStdoutCovCli10(t, func() error {
		return intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covBindingPath)
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"enabled":false`) {
		t.Errorf("patch body wrong: %+v", calls)
	}
	if !strings.Contains(out, "Binding bind-1 on agent "+covBindingAgentID+" updated.") {
		t.Errorf("confirmation missing: %q", out)
	}
}

func TestIntgAgentUpdateBindingRunE_ClearCredentialAndEnvVar(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covBindingPath, clitest.JSONResponse(200, map[string]any{"id": "bind-1"}))
	covSetupCli10(t, s.URL())
	// Empty string credential clears the binding without a lookup.
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "credential", "")
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "env-var-name", "")

	if _, err := captureStdoutCovCli10(t, func() error {
		return intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covBindingPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	body := string(calls[0].Body)
	if !strings.Contains(body, `"credential_id":""`) || !strings.Contains(body, `"env_var_name":""`) {
		t.Errorf("clearing body wrong: %s", body)
	}
}

func TestIntgAgentUpdateBindingRunE_ResolvesCredentialByName(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	credID := "ccred34567890abcdefghij"
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{
		{"id": credID, "name": "anthropic-key"},
	}))
	s.OnPatch(covBindingPath, clitest.JSONResponse(200, map[string]any{"id": "bind-1"}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "credential", "anthropic-key")
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "cred-type", "bearer")
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "cred-header", "X-Api-Key")

	if _, err := captureStdoutCovCli10(t, func() error {
		return intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PATCH", covBindingPath)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	body := string(calls[0].Body)
	for _, want := range []string{`"credential_id":"` + credID + `"`, `"cred_type":"bearer"`, `"cred_header":"X-Api-Key"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s: %s", want, body)
		}
	}
}

func TestIntgAgentUpdateBindingRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestIntgAgentUpdateBindingRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPatch(covBindingPath, clitest.ErrorResponse(404, "binding not found"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, intgAgentUpdateBindingCmd, "enabled", "true")
	err := intgAgentUpdateBindingCmd.RunE(intgAgentUpdateBindingCmd, []string{covBindingAgentID, "bind-1"})
	if err == nil || !strings.Contains(err.Error(), "binding not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}
