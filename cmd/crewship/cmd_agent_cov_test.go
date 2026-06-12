package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAgentListRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []agentListItem{
		{ID: "ca1", Slug: "viktor", AgentRole: "LEAD", Status: "IDLE", CLIAdapter: "CLAUDE_CODE",
			MemoryEnabled: true, Crew: &agentCrewShort{Name: "Backend", Slug: "backend"}},
		{ID: "ca2", Slug: "eva", AgentRole: "AGENT", Status: "IDLE", CLIAdapter: "CODEX_CLI"},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return agentListCmd.RunE(agentListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "viktor") || !strings.Contains(out, "backend") {
		t.Errorf("crewed agent row missing: %q", out)
	}
	// Agent without crew renders "-" and memory off.
	if !strings.Contains(out, "eva") || !strings.Contains(out, "-") {
		t.Errorf("crewless agent row missing dash: %q", out)
	}
}

func TestAgentListRunE_CrewFilterCUID(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []agentListItem{}))
	covSetupCli10(t, s.URL())
	crewID := "ccrew34567890abcdefghij"
	setFlagCovCli10(t, agentListCmd, "crew", crewID)

	if _, err := captureStdoutCovCli10(t, func() error {
		return agentListCmd.RunE(agentListCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("GET", "/api/v1/agents")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "crew_id="+crewID) {
		t.Errorf("crew_id filter not propagated, query=%q", calls[0].Query)
	}
}

func TestAgentListRunE_CrewFilterUnknownSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, agentListCmd, "crew", "ghost-crew")

	err := agentListCmd.RunE(agentListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestAgentListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := agentListCmd.RunE(agentListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestAgentGetRunE_DetailWithRoleTitle(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	roleTitle := "Staff Engineer"
	detail := agentDetailResponse{
		ID: agentID, Name: "Viktor", Slug: "viktor", AgentRole: "LEAD",
		RoleTitle: &roleTitle, Status: "IDLE", CLIAdapter: "CLAUDE_CODE",
		ToolProfile: "CODING", MemoryEnabled: true, TimeoutSeconds: 600,
		CreatedAt: "2026-06-01T00:00:00Z",
		Crew:      &agentCrewShort{Name: "Backend", Slug: "backend"},
	}
	detail.Count.Skills = 2
	detail.Count.Credentials = 1
	s.OnGet("/api/v1/agents/"+agentID, clitest.JSONResponse(200, detail))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return agentGetCmd.RunE(agentGetCmd, []string{agentID})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Viktor", "viktor", "Staff Engineer", "backend", "600s"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail missing %q:\n%s", want, out)
		}
	}
}

func TestAgentGetRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	s.OnGet("/api/v1/agents/"+agentID, clitest.ErrorResponse(404, "agent gone"))
	covSetupCli10(t, s.URL())

	err := agentGetCmd.RunE(agentGetCmd, []string{agentID})
	if err == nil || !strings.Contains(err.Error(), "agent gone") {
		t.Errorf("expected server error surfaced, got %v", err)
	}
}

func TestAgentListRunE_NoWorkspaceAndTransportError(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := agentListCmd.RunE(agentListCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}

	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := agentListCmd.RunE(agentListCmd, nil); err == nil {
		t.Error("expected transport error")
	}
}

func TestAgentListRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.TextResponse(200, `{broken`))
	covSetupCli10(t, s.URL())
	if err := agentListCmd.RunE(agentListCmd, nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestAgentGetRunE_GuardsAndDecodeError(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := agentGetCmd.RunE(agentGetCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := agentGetCmd.RunE(agentGetCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}

	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	s.OnGet("/api/v1/agents/"+agentID, clitest.TextResponse(200, `not json`))
	covSetupCli10(t, s.URL())
	if err := agentGetCmd.RunE(agentGetCmd, []string{agentID}); err == nil {
		t.Error("expected decode error")
	}
}

func TestWarnCoordinatorDeprecated_WarnsOnCoordinator(t *testing.T) {
	setFlagCovCli10(t, agentCreateCmd, "role", "coordinator")
	out, _ := captureStderrCov(t, func() error {
		warnCoordinatorDeprecated(agentCreateCmd, nil)
		return nil
	})
	if !strings.Contains(out, "COORDINATOR role is deprecated") {
		t.Errorf("expected deprecation warning, got %q", out)
	}
}

func TestWarnCoordinatorDeprecated_SilentForLead(t *testing.T) {
	setFlagCovCli10(t, agentCreateCmd, "role", "LEAD")
	out, _ := captureStderrCov(t, func() error {
		warnCoordinatorDeprecated(agentCreateCmd, nil)
		return nil
	})
	if out != "" {
		t.Errorf("LEAD must not warn, got %q", out)
	}
}
