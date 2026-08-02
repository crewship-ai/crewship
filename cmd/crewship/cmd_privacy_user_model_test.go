package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// API↔CLI parity for #1669: every route added under /api/v1/users/me/
// user-model is driven here through the command the agent and the
// operator actually use, not a hand-rolled request.

func TestPrivacyUserModelListRunE_RendersEveryStoredField(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{
		UserID: "u1",
		Exists: true,
		Facts: []userModelFact{
			{Key: "role", Value: "runs the platform team"},
			{Key: "constraint", Value: "commits carry no co-author trailer"},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelListCmd.RunE(privacyUserModelListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"role", "runs the platform team", "constraint", "co-author trailer"} {
		if !strings.Contains(out, want) {
			t.Errorf("user-model list omitted %q:\n%s", want, out)
		}
	}
}

// An empty model must render as an empty readout rather than an error —
// "nothing is stored about you" is the honest answer for a fresh
// operator, and it is the answer the issue's live probe got.
func TestPrivacyUserModelListRunE_EmptyModelIsNotAnError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{
		UserID: "u1", Exists: false, Facts: []userModelFact{},
	}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelListCmd.RunE(privacyUserModelListCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestPrivacyUserModelForgetRunE_TargetsTheNamedField(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model/facts/timezone",
		clitest.JSONResponse(200, userModelResponse{
			Forgot:    "timezone",
			Remaining: []userModelFact{{Key: "role", Value: "runs the platform team"}},
		}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelForgetCmd.RunE(privacyUserModelForgetCmd, []string{"timezone"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model/facts/timezone")); n != 1 {
		t.Errorf("expected 1 DELETE on the named field, got %d", n)
	}
}

// A field name with a space must reach the field route as one path
// segment rather than becoming a malformed request line. url.PathEscape
// is what makes that true; the stub sees the decoded path.
func TestPrivacyUserModelForgetRunE_EscapesTheFieldName(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model/facts/odd name",
		clitest.JSONResponse(200, userModelResponse{Forgot: "odd name"}))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelForgetCmd.RunE(privacyUserModelForgetCmd, []string{"odd name"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model")); n != 0 {
		t.Errorf("the field name collapsed into the whole-model delete route")
	}
}

func TestPrivacyUserModelDeleteRunE_ConfirmsThenPurges(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/user-model", clitest.JSONResponse(200, userModelResponse{Purged: 1}))
	covSetupCli10(t, s.URL())
	_ = privacyUserModelDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = privacyUserModelDeleteCmd.Flags().Set("yes", "false") })

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyUserModelDeleteCmd.RunE(privacyUserModelDeleteCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/user-model")); n != 1 {
		t.Errorf("expected 1 DELETE, got %d", n)
	}
}
