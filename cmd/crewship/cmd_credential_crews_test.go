package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestCredCreateCmd_Crews covers #1083 item 1: `credential create --crews`
// resolves crew slugs to IDs and sends them as crew_ids so the CLI can manage
// crew scoping at parity with the API/UI.
func TestCredCreateCmd_Crews(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew00000000000000000aa", "slug": "backend-team"},
	}))
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "gh-token"}))

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "crews": "backend-team",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/credentials")[0].Body, &body)
	crewIDs, ok := body["crew_ids"].([]any)
	if !ok || len(crewIDs) != 1 || crewIDs[0] != "ccrew00000000000000000aa" {
		t.Errorf("crew_ids not resolved+sent: %v", body["crew_ids"])
	}
}

// TestCredCreateCmd_InvalidScope covers #1083 item 2: an unrecognized
// --scope value must be rejected client-side with a clear error rather than
// being sent to the server, where it would land in the DB and half-orphan
// the credential.
func TestCredCreateCmd_InvalidScope(t *testing.T) {
	covStub(t)
	covResetFlags(t, credCreateCmd)

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "scope": "banana",
	})
	err := credCreateCmd.RunE(credCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--scope must be WORKSPACE or CREW") {
		t.Fatalf("expected --scope validation error, got %v", err)
	}
}

// TestCredCreateCmd_ScopeCaseInsensitive covers #1083 item 2: --scope is
// accepted case-insensitively and normalized to upper case before being
// sent to the server.
func TestCredCreateCmd_ScopeCaseInsensitive(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "gh-token"}))

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "scope": "workspace",
	})
	if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/credentials")[0].Body, &body)
	if body["scope"] != "WORKSPACE" {
		t.Errorf("scope not normalized: %v", body["scope"])
	}
}

// TestCredCreateCmd_ScopeWorkspaceWithCrews_Warns covers #1083 item 2: the
// server silently overrides scope to CREW whenever --crews is set, even if
// the caller explicitly asked for --scope WORKSPACE. The CLI can't stop
// that (the server's behaviour wins), but it must warn on stderr so the
// combination isn't silently surprising.
func TestCredCreateCmd_ScopeWorkspaceWithCrews_Warns(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credCreateCmd)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew00000000000000000aa", "slug": "backend-team"},
	}))
	stub.OnPost("/api/v1/credentials",
		clitest.JSONResponse(201, map[string]string{"id": covCredIDCli3, "name": "gh-token"}))

	covSetFlags(t, credCreateCmd, map[string]string{
		"name": "gh-token", "type": "API_KEY", "provider": "NONE",
		"value": "secret-v", "crews": "backend-team", "scope": "WORKSPACE",
	})
	stderr := covCaptureStderr(t, func() {
		if err := credCreateCmd.RunE(credCreateCmd, nil); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "--scope WORKSPACE is ignored when --crews is set") {
		t.Errorf("expected override warning on stderr, got %q", stderr)
	}
}

// TestCredUpdateCmd_InvalidScope covers #1083 item 2 on the update path.
func TestCredUpdateCmd_InvalidScope(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{
		"id": covCredIDCli3, "name": "gh-token",
	}))

	covSetFlags(t, credUpdateCmd, map[string]string{"scope": "banana"})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--scope must be WORKSPACE or CREW") {
		t.Fatalf("expected --scope validation error, got %v", err)
	}
}

// TestCredUpdateCmd_EmptyScope_Rejected covers #1083 item 2: passing
// --scope "" (flags.Changed is still true) must be rejected rather than
// sent to the server as an empty string.
func TestCredUpdateCmd_EmptyScope_Rejected(t *testing.T) {
	covStub(t)
	covResetFlags(t, credUpdateCmd)

	covSetFlags(t, credUpdateCmd, map[string]string{"scope": ""})
	err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "--scope cannot be empty") {
		t.Fatalf("expected empty --scope rejection, got %v", err)
	}
}

// TestCredUpdateCmd_ScopeWorkspaceWithCrews_Warns mirrors
// TestCredCreateCmd_ScopeWorkspaceWithCrews_Warns for `credential update`.
func TestCredUpdateCmd_ScopeWorkspaceWithCrews_Warns(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credUpdateCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{
		"id": covCredIDCli3, "name": "gh-token",
	}))
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": "ccrew00000000000000000aa", "slug": "backend-team"},
	}))
	stub.OnPatch("/api/v1/credentials/"+covCredIDCli3, clitest.JSONResponse(200, map[string]string{
		"id": covCredIDCli3, "name": "gh-token",
	}))

	covSetFlags(t, credUpdateCmd, map[string]string{
		"crews": "backend-team", "scope": "WORKSPACE",
	})
	stderr := covCaptureStderr(t, func() {
		if err := credUpdateCmd.RunE(credUpdateCmd, []string{covCredIDCli3}); err != nil {
			t.Fatalf("RunE: %v", err)
		}
	})
	if !strings.Contains(stderr, "--scope WORKSPACE is ignored when --crews is set") {
		t.Errorf("expected override warning on stderr, got %q", stderr)
	}
}
