package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covCrewIDCli10 = "ccrew34567890abcdefghij"

func TestCrewMemberListRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.JSONResponse(200, []map[string]any{
		{
			"id":        "cm-1",
			"user":      map[string]string{"id": "u-1", "email": "pavel@example.com", "full_name": "Pavel"},
			"joined_at": "2026-06-01T00:00:00Z",
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"cm-1", "Pavel", "pavel@example.com", "2026-06-01"} {
		if !strings.Contains(out, want) {
			t.Errorf("member row missing %q:\n%s", want, out)
		}
	}
}

func TestCrewMemberListRunE_ResolvesSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli10, "slug": "backend"},
	}))
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.JSONResponse(200, []map[string]any{}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return crewMemberListCmd.RunE(crewMemberListCmd, []string{"backend"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli10+"/members")); got != 1 {
		t.Errorf("resolved-member GET calls = %d, want 1", got)
	}
}

func TestCrewMemberListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestCrewMemberListRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.ErrorResponse(500, "boom"))
	covSetupCli10(t, s.URL())
	if err := crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected error from 500")
	}
}

func TestCrewMemberAddRunE_PostsUserID(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.JSONResponse(201, map[string]string{"id": "cm-9"}))
	covSetupCli10(t, s.URL())

	stderr, err := captureStderrCov(t, func() error {
		return crewMemberAddCmd.RunE(crewMemberAddCmd, []string{covCrewIDCli10, "u-42"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/crews/"+covCrewIDCli10+"/members")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"user_id":"u-42"`) {
		t.Errorf("add body wrong: %+v", calls)
	}
	if !strings.Contains(stderr, "Member added to crew.") {
		t.Errorf("success message missing: %q", stderr)
	}
}

func TestCrewMemberAddRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.ErrorResponse(409, "already a member"))
	covSetupCli10(t, s.URL())
	err := crewMemberAddCmd.RunE(crewMemberAddCmd, []string{covCrewIDCli10, "u-42"})
	if err == nil || !strings.Contains(err.Error(), "already a member") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

func TestCrewMemberRemoveRunE_Deletes(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/crews/"+covCrewIDCli10+"/members/cm-1", clitest.EmptyResponse(204))
	covSetupCli10(t, s.URL())

	stderr, err := captureStderrCov(t, func() error {
		return crewMemberRemoveCmd.RunE(crewMemberRemoveCmd, []string{covCrewIDCli10, "cm-1"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("DELETE", "/api/v1/crews/"+covCrewIDCli10+"/members/cm-1")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
	if !strings.Contains(stderr, "Member removed from crew.") {
		t.Errorf("success message missing: %q", stderr)
	}
}

func TestCrewMemberListRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := crewMemberListCmd.RunE(crewMemberListCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestCrewMemberListRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews/"+covCrewIDCli10+"/members", clitest.TextResponse(200, `[{`))
	covSetupCli10(t, s.URL())
	if err := crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected decode error")
	}
}

func TestCrewMemberAddRemoveRunE_TransportError(t *testing.T) {
	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	if err := crewMemberAddCmd.RunE(crewMemberAddCmd, []string{covCrewIDCli10, "u-1"}); err == nil {
		t.Error("add: expected transport error")
	}
	if err := crewMemberRemoveCmd.RunE(crewMemberRemoveCmd, []string{covCrewIDCli10, "cm-1"}); err == nil {
		t.Error("remove: expected transport error")
	}
}

func TestCrewMemberRemoveRunE_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := crewMemberRemoveCmd.RunE(crewMemberRemoveCmd, []string{covCrewIDCli10, "cm-1"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestCrewMemberListRunE_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestCrewMemberListRunE_TransportError(t *testing.T) {
	s := clitest.NewStubServer()
	s.Close() // connection refused → client.Get error branch
	covSetupCli10(t, s.URL())
	if err := crewMemberListCmd.RunE(crewMemberListCmd, []string{covCrewIDCli10}); err == nil {
		t.Error("expected transport error")
	}
}

func TestCrewMemberAddRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := crewMemberAddCmd.RunE(crewMemberAddCmd, []string{"ghost", "u-1"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

func TestCrewMemberAddRunE_NoWorkspace(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := crewMemberAddCmd.RunE(crewMemberAddCmd, []string{covCrewIDCli10, "u-1"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestCrewMemberRemoveRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/crews/"+covCrewIDCli10+"/members/cm-x", clitest.ErrorResponse(404, "member not found"))
	covSetupCli10(t, s.URL())
	err := crewMemberRemoveCmd.RunE(crewMemberRemoveCmd, []string{covCrewIDCli10, "cm-x"})
	if err == nil || !strings.Contains(err.Error(), "member not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

func TestCrewMemberRemoveRunE_UnknownCrewSlug(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := crewMemberRemoveCmd.RunE(crewMemberRemoveCmd, []string{"ghost", "cm-1"})
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}
