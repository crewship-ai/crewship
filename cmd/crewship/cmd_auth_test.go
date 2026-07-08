package main

import (
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// CLI parity for `crewship auth passwd` (#867.1). Drives the command in
// scripted (piped-stdin) mode against a stub and asserts it POSTs the
// password body to the user-scoped endpoint. Passwords are never flags,
// so scripted input comes on stdin: current on line 1, new on line 2.

// withStdin swaps os.Stdin for a pipe carrying `input` for the duration
// of fn. Not parallel-safe (os.Stdin is process-global).
func withStdin(t *testing.T, input string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	go func() {
		_, _ = w.WriteString(input)
		_ = w.Close()
	}()
	defer func() { os.Stdin = orig; _ = r.Close() }()
	fn()
}

func TestAuthPasswd_RunE_PostsPasswordChange(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/users/me/password",
		clitest.JSONResponse(200, map[string]any{"success": true, "sessions_revoked": 2}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		withStdin(t, "oldpassword1\nbrandnew123\n", func() {
			if err := authPasswdCmd.RunE(authPasswdCmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
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

	var err error
	withStdin(t, "oldpassword1\nshort\n", func() {
		err = authPasswdCmd.RunE(authPasswdCmd, nil)
	})
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

	captureStdoutCovCli2(t, func() {
		withStdin(t, "wrong\nbrandnew123\n", func() {
			if err := authPasswdCmd.RunE(authPasswdCmd, nil); err == nil {
				t.Errorf("expected error on 401 from server")
			}
		})
	})
}
