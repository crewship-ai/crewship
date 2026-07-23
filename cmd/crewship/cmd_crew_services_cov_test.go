package main

// Drives the real `crewship crew services` command against a stub API
// server — the supported agent contract (API↔CLI parity), not a
// hand-rolled HTTP request. Mirrors cmd_crew_members_cov_test.go.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestCrewServicesRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/services", clitest.JSONResponse(200, map[string]any{
		"services": []map[string]any{
			{
				"name":   "postgres",
				"image":  "postgres:16",
				"type":   "postgres",
				"status": "running",
				"ports":  []string{"5432/tcp"},
			},
			{
				"name":   "redis",
				"image":  "redis:7-alpine",
				"type":   "redis",
				"status": "stopped",
				"ports":  []string{"6379/tcp"},
			},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"postgres", "postgres:16", "running", "5432/tcp", "redis", "stopped", "6379/tcp"} {
		if !strings.Contains(out, want) {
			t.Errorf("service row missing %q:\n%s", want, out)
		}
	}
}

func TestCrewServicesRunE_ResolvesSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli10, "slug": "backend"},
	}))
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/services", clitest.JSONResponse(200, map[string]any{"services": []map[string]any{}}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return crewServicesCmd.RunE(crewServicesCmd, []string{"backend"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli10+"/services")); got != 1 {
		t.Errorf("resolved-services GET calls = %d, want 1", got)
	}
}

func TestCrewServicesRunE_EmptyList(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/services", clitest.JSONResponse(200, map[string]any{"services": []map[string]any{}}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "postgres") || strings.Contains(out, "redis") {
		t.Errorf("expected no service rows: %q", out)
	}
}

func TestCrewServicesRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestCrewServicesRunE_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestCrewServicesRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/services", clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected error from 500")
	}
}

func TestCrewServicesRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := crewServicesCmd.RunE(crewServicesCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestCrewServicesRunE_TransportError(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close() // connection refused -> client.Get error branch
	covSetupCli10(t, s.URL())
	if err := crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected transport error")
	}
}

func TestCrewServicesRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/services", clitest.TextResponse(200, `{"services":[{`))
	covSetupCli10(t, s.URL())
	if err := crewServicesCmd.RunE(crewServicesCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected decode error")
	}
}
