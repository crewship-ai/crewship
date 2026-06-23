package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestExposeListRunE_HappyPathWithStatusFilter(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	revokedAt := "2026-06-10T00:00:00Z"
	reason := "demo over"
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/port-expose", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "cexp1234567890abcdefghi", "agent_slug": "viktor", "container_port": 3000,
			"description": strings.Repeat("d", 45), "status": "ACTIVE",
			"expires_at": "2026-06-12T00:00:00Z", "created_at": "2026-06-11T00:00:00Z",
		},
		{
			"id": "cexp1234567890abcdefghj", "agent_slug": "eva", "container_port": 8080,
			"status": "REVOKED", "expires_at": "2026-06-12T00:00:00Z", "created_at": "2026-06-11T00:00:00Z",
			"revoked_at": revokedAt, "revoked_reason": reason,
		},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeListCmd, "crew", covCrewIDCli10)
	setFlagCovCli10(t, exposeListCmd, "status", "ALL")

	out, err := captureStdoutCovCli10(t, func() error {
		return exposeListCmd.RunE(exposeListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"viktor", "3000", "ACTIVE", "REVOKED", "..."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	calls := s.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli10+"/port-expose")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "status=all") {
		t.Errorf("status filter should lowercase into query: %+v", calls)
	}
}

func TestExposeListRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/port-expose", clitest.ErrorResponse(403, "crew access denied"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeListCmd, "crew", covCrewIDCli10)

	err := exposeListCmd.RunE(exposeListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew access denied") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

func TestExposeRevokeRunE_PostsReason(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/"+covCrewIDCli10+"/port-expose/exp-1/revoke",
		clitest.JSONResponse(200, map[string]string{"status": "REVOKED"}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeRevokeCmd, "crew", covCrewIDCli10)
	setFlagCovCli10(t, exposeRevokeCmd, "reason", "leaked URL")
	setFlagCovCli10(t, exposeRevokeCmd, "yes", "true")

	stderr, err := captureStderrCov(t, func() error {
		return exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli10+"/port-expose/exp-1/revoke")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"reason":"leaked URL"`) {
		t.Errorf("revoke body wrong: %+v", calls)
	}
	if !strings.Contains(stderr, "Exposure exp-1 revoked.") {
		t.Errorf("success message missing: %q", stderr)
	}
}

func TestExposeRevokeRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeRevokeCmd, "crew", "ghost")
	setFlagCovCli10(t, exposeRevokeCmd, "yes", "true")

	err := exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-1"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestExposeListRunE_NoWorkspaceCov(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := exposeListCmd.RunE(exposeListCmd, nil); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestExposeRevokeRunE_NoWorkspaceCov(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-1"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestExposeRevokeRunE_EmptyReasonOmittedFromBody(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/"+covCrewIDCli10+"/port-expose/exp-2/revoke",
		clitest.JSONResponse(200, map[string]string{"status": "REVOKED"}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeRevokeCmd, "crew", covCrewIDCli10)
	setFlagCovCli10(t, exposeRevokeCmd, "yes", "true")

	if _, err := captureStderrCov(t, func() error {
		return exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-2"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli10+"/port-expose/exp-2/revoke")
	if len(calls) != 1 || strings.Contains(string(calls[0].Body), "reason") {
		t.Errorf("empty reason must be omitted: %+v", calls)
	}
}

func TestExposeListRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/port-expose", clitest.TextResponse(200, `[{`))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeListCmd, "crew", covCrewIDCli10)
	if err := exposeListCmd.RunE(exposeListCmd, nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestExposeRevokeRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/"+covCrewIDCli10+"/port-expose/exp-1/revoke",
		clitest.ErrorResponse(404, "exposure not found"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, exposeRevokeCmd, "crew", covCrewIDCli10)
	setFlagCovCli10(t, exposeRevokeCmd, "yes", "true")

	err := exposeRevokeCmd.RunE(exposeRevokeCmd, []string{"exp-1"})
	if err == nil || !strings.Contains(err.Error(), "exposure not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}
