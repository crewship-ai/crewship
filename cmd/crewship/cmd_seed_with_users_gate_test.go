package main

// `crewship seed --with-users` needs CREWSHIP_ALLOW_SIGNUP=true on the SERVER.
// Without it all four signups answer 403, the fixture lands nobody — and the
// command still exited 0, printing the reason to stderr where nothing reads
// it. The nightly harness matrix seeded exactly that way for as long as the
// flag has existed, so every suite that depends on a second identity skipped
// against a workspace it had been told was seeded (#1829).
//
// Two gates hold it now:
//   - a preflight against GET /api/v1/system/setup-status, so the cause is
//     named before four pointless signups rather than inferred afterwards;
//   - a verdict: a fixture that placed nobody is a failure, not a notice.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const setupStatusPath = "/api/v1/system/setup-status"

// TestSeedRBACUsers_RefusesWhenSignupIsDisabled is the red-first case: the
// exact CI configuration, which used to report success.
func TestSeedRBACUsers_RefusesWhenSignupIsDisabled(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]any{
		"needs_bootstrap": false, "allow_signup": false,
	}))
	// Registered but unreachable if the preflight does its job.
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.ErrorResponse(403, "Registration is disabled."))

	var err error
	out := captureStdoutCovCli2(t, func() {
		err = seedRBACUsers(context.Background(), newSeedClient(stub))
	})
	if err == nil {
		t.Fatalf("--with-users against a server with signup disabled must fail, got nil\n%s", out)
	}
	if !strings.Contains(err.Error(), "CREWSHIP_ALLOW_SIGNUP") {
		t.Errorf("error %q does not name the server flag that has to change", err.Error())
	}
	if n := len(stub.CallsFor("POST", signupPath)); n != 0 {
		t.Errorf("signups = %d — the preflight must refuse before spending them", n)
	}
}

// allow_signup=true is the supported configuration and must not be slowed
// down or refused by the preflight.
func TestSeedRBACUsers_ProceedsWhenSignupIsEnabled(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]any{"allow_signup": true}))
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))

	var err error
	_ = captureStdoutCovCli2(t, func() {
		err = seedRBACUsers(context.Background(), newSeedClient(stub))
	})
	if err != nil {
		t.Fatalf("seedRBACUsers: %v", err)
	}
	if n := len(stub.CallsFor("POST", signupPath)); n != len(demoUsers) {
		t.Errorf("signups = %d, want %d", n, len(demoUsers))
	}
}

// The preflight is advisory, not a new dependency: a server that does not
// answer setup-status (older build, proxy) must not block the fixture. The
// zero-placed verdict still catches the real failure downstream.
func TestSeedRBACUsers_UnreadableSetupStatusDoesNotBlock(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	// setup-status unregistered → default 404.
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.JSONResponse(202, map[string]any{"ok": true}))
	stub.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))

	var err error
	_ = captureStdoutCovCli2(t, func() {
		err = seedRBACUsers(context.Background(), newSeedClient(stub))
	})
	if err != nil {
		t.Fatalf("an unreadable setup-status must not block the fixture: %v", err)
	}
	if n := len(stub.CallsFor("POST", signupPath)); n != len(demoUsers) {
		t.Errorf("signups = %d, want %d", n, len(demoUsers))
	}
}

// The verdict gate, independent of the preflight: signup is advertised as
// open, every placement still fails, nobody lands. Exiting 0 there is what
// let CI believe it had a second identity.
func TestSeedRBACUsers_ZeroPlacedIsAFailure(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]any{"allow_signup": true}))
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	stub.OnPost(signupPath, clitest.ErrorResponse(500, "signup broken"))

	var err error
	out := captureStdoutCovCli2(t, func() {
		err = seedRBACUsers(context.Background(), newSeedClient(stub))
	})
	if err == nil {
		t.Fatalf("a fixture that placed nobody must fail:\n%s", out)
	}
	if !strings.Contains(err.Error(), "no RBAC fixture users") {
		t.Errorf("error = %q", err.Error())
	}
	// Per-user failures must still be reported individually — the loop
	// keeps going, only the verdict at the end changed.
	if !strings.Contains(out, "signup HTTP 500") {
		t.Errorf("per-user failure lines missing:\n%s", out)
	}
}

// A partial fixture stays a success: three of four users is still usable, and
// the per-user lines say which one is missing.
func TestSeedRBACUsers_PartialPlacementStillSucceeds(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet(setupStatusPath, clitest.JSONResponse(200, map[string]any{"allow_signup": true}))
	stub.OnPost(invitationsPath, clitest.JSONResponse(201, map[string]string{"id": "inv1"}))
	seen := 0
	stub.OnPost(signupPath, func(*http.Request, []byte) (int, []byte, string) {
		seen++
		if seen == 1 {
			return 500, []byte(`{"error":"transient"}`), "application/json"
		}
		return 202, []byte(`{"ok":true}`), "application/json"
	})
	stub.OnGet(adminUsers, clitest.JSONResponse(200, fixtureRoster()))

	var err error
	_ = captureStdoutCovCli2(t, func() {
		err = seedRBACUsers(context.Background(), newSeedClient(stub))
	})
	if err != nil {
		t.Fatalf("a partial fixture must not fail the seed: %v", err)
	}
}
