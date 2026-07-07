package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for `crewship auth passwd` (#867.1). Drives the command with
// --current/--new flags (the non-interactive path) against a stub and
// asserts it POSTs the password body to the user-scoped endpoint.

func TestAuthPasswd_RunE_PostsPasswordChange(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/users/me/password",
		clitest.JSONResponse(200, map[string]any{"success": true, "sessions_revoked": 2}))
	setStubCLI(t, stub.URL())

	if err := authPasswdCmd.Flags().Set("current", "oldpassword1"); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if err := authPasswdCmd.Flags().Set("new", "brandnew123"); err != nil {
		t.Fatalf("set new: %v", err)
	}
	t.Cleanup(func() {
		_ = authPasswdCmd.Flags().Set("current", "")
		_ = authPasswdCmd.Flags().Set("new", "")
	})

	out := captureStdoutCovCli2(t, func() {
		if err := authPasswdCmd.RunE(authPasswdCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/users/me/password")
	if len(calls) != 1 {
		t.Fatalf("want exactly 1 POST, got %d", len(calls))
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body.CurrentPassword != "oldpassword1" || body.NewPassword != "brandnew123" {
		t.Fatalf("body = %+v, want current/new populated", body)
	}
	if !strings.Contains(strings.ToLower(out), "password changed") {
		t.Errorf("expected success message, got: %s", out)
	}
}

// A too-short new password is rejected locally, before any server call.
func TestAuthPasswd_RunE_RejectsShortNew(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	_ = authPasswdCmd.Flags().Set("current", "oldpassword1")
	_ = authPasswdCmd.Flags().Set("new", "short")
	t.Cleanup(func() {
		_ = authPasswdCmd.Flags().Set("current", "")
		_ = authPasswdCmd.Flags().Set("new", "")
	})

	err := authPasswdCmd.RunE(authPasswdCmd, nil)
	if err == nil {
		t.Fatalf("expected error for short new password")
	}
	if len(stub.CallsFor("POST", "/api/v1/users/me/password")) != 0 {
		t.Errorf("no server call should be made for a too-short password")
	}
}

// A server-side rejection (401 wrong current password) surfaces as an error.
func TestAuthPasswd_RunE_SurfacesServerError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/users/me/password",
		clitest.ErrorResponse(401, "current password is incorrect"))
	setStubCLI(t, stub.URL())

	_ = authPasswdCmd.Flags().Set("current", "wrong")
	_ = authPasswdCmd.Flags().Set("new", "brandnew123")
	t.Cleanup(func() {
		_ = authPasswdCmd.Flags().Set("current", "")
		_ = authPasswdCmd.Flags().Set("new", "")
	})

	captureStdoutCovCli2(t, func() {
		if err := authPasswdCmd.RunE(authPasswdCmd, nil); err == nil {
			t.Errorf("expected error on 401 from server")
		}
	})
}
