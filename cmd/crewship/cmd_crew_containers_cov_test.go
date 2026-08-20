package main

// Drives the real `crewship crew containers` command against a stub API
// server — the supported agent contract (API↔CLI parity), not a hand-rolled
// HTTP request. Mirrors cmd_crew_services_cov_test.go.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCrewContainersRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/containers", clitest.JSONResponse(200, map[string]any{
		"containers": []map[string]any{
			{
				"name":        "crewship-team-backend-" + covCrewIDCli10,
				"image":       "crewship/agent:latest",
				"kind":        "crew",
				"status":      "running",
				"cpu_percent": 3.5,
				"memory_mb":   412,
				"agent_count": 4,
			},
			{
				// A stopped sidecar: every number is null, and none of them
				// may print as a zero.
				"name":        "crewship-svc-backend-" + covCrewIDCli10 + "-postgres",
				"image":       "postgres:16",
				"kind":        "sidecar",
				"status":      "stopped",
				"cpu_percent": nil,
				"memory_mb":   nil,
				"agent_count": nil,
			},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"crewship-team-backend-", "crewship/agent:latest", "crew", "running", "3.5%", "412 MB", "4",
		"postgres:16", "sidecar", "stopped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("container row missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "0.0%") {
		t.Errorf("an unmeasured CPU reading printed as 0.0%%:\n%s", out)
	}
	if strings.Contains(out, "0 MB") {
		t.Errorf("an unmeasured memory reading printed as 0 MB:\n%s", out)
	}
}

func TestCrewContainersRunE_ResolvesSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli10, "slug": "backend"},
	}))
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/containers", clitest.JSONResponse(200, map[string]any{"containers": []map[string]any{}}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return crewContainersCmd.RunE(crewContainersCmd, []string{"backend"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli10+"/containers")); got != 1 {
		t.Errorf("resolved-containers GET calls = %d, want 1", got)
	}
}

func TestCrewContainersRunE_EmptyList(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/containers", clitest.JSONResponse(200, map[string]any{"containers": []map[string]any{}}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "crewship-team-") {
		t.Errorf("expected no container rows: %q", out)
	}
}

func TestCrewContainersRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestCrewContainersRunE_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestCrewContainersRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/containers", clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected error from 500")
	}
}

func TestCrewContainersRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := crewContainersCmd.RunE(crewContainersCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestCrewContainersRunE_TransportError(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close() // connection refused -> client.Get error branch
	covSetupCli10(t, s.URL())
	if err := crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected transport error")
	}
}

func TestCrewContainersRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/containers", clitest.TextResponse(200, `{"containers":[{`))
	covSetupCli10(t, s.URL())
	if err := crewContainersCmd.RunE(crewContainersCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected decode error")
	}
}
