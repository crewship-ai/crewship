package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestInitRunE_RequiresEmail(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--email is required") {
		t.Errorf("expected email guard, got %v", err)
	}
}

func TestInitRunE_RequiresName(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected name guard, got %v", err)
	}
}

func TestInitRunE_RejectsShortPassword(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	setFlagCovCli10(t, initCmd, "password", "short")
	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "at least 8 characters") {
		t.Errorf("expected length guard, got %v", err)
	}
}

func TestInitRunE_PasswordFlagAndStdinMutuallyExclusive(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	setFlagCovCli10(t, initCmd, "password", "longenough")
	setFlagCovCli10(t, initCmd, "password-stdin", "true")
	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected exclusivity guard, got %v", err)
	}
}

func TestInitRunE_HappyPathBootstrap(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.JSONResponse(201, map[string]string{
		"user_id": "u-1", "email": "admin@example.com",
		"workspace_id": "ws-1", "cli_token": "crewship_cli_tok",
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, initCmd, "server", s.URL())
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin User")
	setFlagCovCli10(t, initCmd, "password", "password123")

	stderr, err := captureStderrCov(t, func() error {
		return initCmd.RunE(initCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/bootstrap")
	if len(calls) != 1 {
		t.Fatalf("bootstrap calls = %d", len(calls))
	}
	body := string(calls[0].Body)
	for _, want := range []string{`"email":"admin@example.com"`, `"full_name":"Admin User"`, `"password":"password123"`} {
		if !strings.Contains(body, want) {
			t.Errorf("bootstrap body missing %s: %s", want, body)
		}
	}
	for _, want := range []string{"Crewship initialized!", "admin@example.com", "ws-1", "crewship_cli_tok", "crewship login --server " + s.URL()} {
		if !strings.Contains(stderr, want) {
			t.Errorf("init output missing %q:\n%s", want, stderr)
		}
	}
}

func TestInitRunE_TransportError(t *testing.T) {
	dead := clitest.NewStubServer()
	dead.Close()
	covSetupCli10(t, dead.URL())
	setFlagCovCli10(t, initCmd, "server", dead.URL())
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	setFlagCovCli10(t, initCmd, "password", "password123")

	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "bootstrap request failed") {
		t.Errorf("expected wrapped transport error, got %v", err)
	}
}

func TestInitRunE_MalformedBootstrapResponse(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.TextResponse(200, `not json`))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, initCmd, "server", s.URL())
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	setFlagCovCli10(t, initCmd, "password", "password123")

	if err := initCmd.RunE(initCmd, nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestInitRunE_BootstrapConflictSurfaced(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/bootstrap", clitest.ErrorResponse(409, "instance already initialized"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, initCmd, "server", s.URL())
	setFlagCovCli10(t, initCmd, "email", "admin@example.com")
	setFlagCovCli10(t, initCmd, "name", "Admin")
	setFlagCovCli10(t, initCmd, "password", "password123")

	err := initCmd.RunE(initCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}
