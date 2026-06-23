package main

import (
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSessionListRunE_TableWithStaleFooter(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	now := time.Now().UTC()
	s.OnGet("/api/v1/auth/sessions", clitest.JSONResponse(200, []map[string]any{
		{"id": "sess_current", "created_at": now.Format(time.RFC3339), "last_used_at": now.Format(time.RFC3339),
			"user_agent": "Mozilla/5.0", "ip": "10.0.0.1", "is_current": true},
		{"id": "sess_stale_000000000", "created_at": now.AddDate(0, -3, 0).Format(time.RFC3339),
			"last_used_at": now.AddDate(0, -2, 0).Format(time.RFC3339),
			"user_agent":   "old-phone", "ip": "", "is_current": false},
	}))
	covSetupCli10(t, s.URL())

	var stderr string
	stdout, err := captureStdoutCovCli10(t, func() error {
		var runErr error
		stderr, runErr = captureStderrCov(t, func() error {
			return sessionListCmd.RunE(sessionListCmd, nil)
		})
		return runErr
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(stdout, "current") || !strings.Contains(stdout, "stale") {
		t.Errorf("status column wrong:\n%s", stdout)
	}
	// Empty IP renders as dash.
	if !strings.Contains(stdout, "-") {
		t.Errorf("empty IP should render dash:\n%s", stdout)
	}
	if !strings.Contains(stderr, "1 stale session(s) found.") {
		t.Errorf("stale footer missing on stderr: %q", stderr)
	}
}

func TestSessionListRunE_StaleCheckDisabled(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	old := time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339)
	s.OnGet("/api/v1/auth/sessions", clitest.JSONResponse(200, []map[string]any{
		{"id": "sess_old", "created_at": old, "last_used_at": old, "user_agent": "ua", "ip": "1.2.3.4", "is_current": false},
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, sessionListCmd, "warn-stale-days", "0")

	stdout, err := captureStdoutCovCli10(t, func() error {
		return sessionListCmd.RunE(sessionListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(stdout, "stale") {
		t.Errorf("warn-stale-days=0 must disable stale flagging:\n%s", stdout)
	}
	if !strings.Contains(stdout, "active") {
		t.Errorf("expected active status:\n%s", stdout)
	}
}

func TestSessionListRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := sessionListCmd.RunE(sessionListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestSessionListRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/auth/sessions", clitest.ErrorResponse(500, "session store down"))
	covSetupCli10(t, s.URL())
	if err := sessionListCmd.RunE(sessionListCmd, nil); err == nil {
		t.Error("expected error from 500")
	}
}

func TestSessionRevokeRunE_HappyPath(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/auth/sessions/sess_1/revoke", clitest.JSONResponse(200, map[string]any{
		"ok": true, "id": "sess_1", "is_current": false,
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, sessionRevokeCmd, "yes", "true")

	var stderr string
	stdout, err := captureStdoutCovCli10(t, func() error {
		var runErr error
		stderr, runErr = captureStderrCov(t, func() error {
			return sessionRevokeCmd.RunE(sessionRevokeCmd, []string{"sess_1"})
		})
		return runErr
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(s.CallsFor("POST", "/api/v1/auth/sessions/sess_1/revoke")); got != 1 {
		t.Errorf("revoke POST calls = %d, want 1", got)
	}
	if !strings.Contains(stderr, "Session sess_1 revoked.") {
		t.Errorf("confirmation missing: %q", stderr)
	}
	if strings.Contains(stdout, "current session") {
		t.Errorf("non-current revoke must not warn: %q", stdout)
	}
}

func TestSessionRevokeRunE_CurrentSessionWarns(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/auth/sessions/sess_me/revoke", clitest.JSONResponse(200, map[string]any{
		"ok": true, "id": "sess_me", "is_current": true,
	}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, sessionRevokeCmd, "yes", "true")

	out, err := captureStdoutCovCli10(t, func() error {
		return sessionRevokeCmd.RunE(sessionRevokeCmd, []string{"sess_me"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "that was your current session") {
		t.Errorf("current-session warning missing: %q", out)
	}
}

func TestSessionRevokeRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := sessionRevokeCmd.RunE(sessionRevokeCmd, []string{"x"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestSessionRevokeRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/auth/sessions/sess_x/revoke", clitest.ErrorResponse(404, "no such session"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, sessionRevokeCmd, "yes", "true")
	err := sessionRevokeCmd.RunE(sessionRevokeCmd, []string{"sess_x"})
	if err == nil || !strings.Contains(err.Error(), "no such session") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}
