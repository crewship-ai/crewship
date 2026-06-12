package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// Guard rails shared by every credential read command.

func TestCredentialCmds_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{
		{name: "list", cmd: credListCmd},
		{name: "get", cmd: credGetCmd, args: []string{covCredIDCli3}},
		{name: "rotations", cmd: credRotationsCmd, args: []string{covCredIDCli3}},
		{name: "audit", cmd: credAuditCmd, args: []string{covCredIDCli3}},
		{name: "test-stored", cmd: credTestStoredCmd, args: []string{covCredIDCli3}},
		{name: "default-env-var", cmd: credDefaultEnvVarCmd},
	})
}

func TestCredentialCmds_NoWorkspace(t *testing.T) {
	covRunNoWorkspace(t, []covCmdCase{
		{name: "list", cmd: credListCmd},
		{name: "get", cmd: credGetCmd, args: []string{covCredIDCli3}},
		{name: "rotations", cmd: credRotationsCmd, args: []string{covCredIDCli3}},
		{name: "audit", cmd: credAuditCmd, args: []string{covCredIDCli3}},
		{name: "test-stored", cmd: credTestStoredCmd, args: []string{covCredIDCli3}},
	})
}

func TestCredentialCmds_TransportError(t *testing.T) {
	covRunTransportError(t, []covCmdCase{
		{name: "list", cmd: credListCmd},
		{name: "get by id", cmd: credGetCmd, args: []string{covCredIDCli3}},
		// Non-CUID arg forces resolveCredentialID's own GET, covering its
		// transport-error return.
		{name: "get by name resolve", cmd: credGetCmd, args: []string{"by-name"}},
		{name: "rotations", cmd: credRotationsCmd, args: []string{covCredIDCli3}},
		{name: "audit", cmd: credAuditCmd, args: []string{covCredIDCli3}},
		{name: "test-stored", cmd: credTestStoredCmd, args: []string{covCredIDCli3}},
		{name: "default-env-var", cmd: credDefaultEnvVarCmd, flags: map[string]string{"provider": "GITHUB"}},
	})
}

// Malformed 200 responses must error cleanly, never panic.

func TestCredentialCmds_MalformedJSON(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		run    func() error
	}{
		{"list", "GET", "/api/v1/credentials",
			func() error { return credListCmd.RunE(credListCmd, nil) }},
		{"get", "GET", "/api/v1/credentials/" + covCredIDCli3,
			func() error { return credGetCmd.RunE(credGetCmd, []string{covCredIDCli3}) }},
		{"rotations", "GET", "/api/v1/credentials/" + covCredIDCli3 + "/rotations",
			func() error { return credRotationsCmd.RunE(credRotationsCmd, []string{covCredIDCli3}) }},
		{"audit", "GET", "/api/v1/credentials/" + covCredIDCli3 + "/audit",
			func() error { return credAuditCmd.RunE(credAuditCmd, []string{covCredIDCli3}) }},
		{"test-stored", "POST", "/api/v1/credentials/" + covCredIDCli3 + "/test",
			func() error { return credTestStoredCmd.RunE(credTestStoredCmd, []string{covCredIDCli3}) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			stub.On(tc.method, tc.path, clitest.TextResponse(200, "definitely not json"))
			if err := tc.run(); err == nil {
				t.Fatal("expected decode error on malformed body, got nil")
			}
		})
	}
}

func TestCredDefaultEnvVarCmd_ErrorPaths(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDefaultEnvVarCmd)
		stub.OnGet("/api/v1/credentials/default-env-var", clitest.ErrorResponse(400, "unknown provider"))
		covSetFlags(t, credDefaultEnvVarCmd, map[string]string{"provider": "NOPE"})
		err := credDefaultEnvVarCmd.RunE(credDefaultEnvVarCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown provider") {
			t.Fatalf("expected 400 error, got %v", err)
		}
	})
	t.Run("malformed response", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, credDefaultEnvVarCmd)
		stub.OnGet("/api/v1/credentials/default-env-var", clitest.TextResponse(200, "{broken"))
		covSetFlags(t, credDefaultEnvVarCmd, map[string]string{"provider": "GITHUB"})
		if err := credDefaultEnvVarCmd.RunE(credDefaultEnvVarCmd, nil); err == nil {
			t.Fatal("expected decode error")
		}
	})
}

func TestCredRotationsCmd_ServerError(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3+"/rotations",
		clitest.ErrorResponse(403, "not allowed"))
	err := credRotationsCmd.RunE(credRotationsCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected 403, got %v", err)
	}
}

func TestCredAuditCmd_ServerError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, credAuditCmd)
	stub.OnGet("/api/v1/credentials/"+covCredIDCli3+"/audit",
		clitest.ErrorResponse(500, "audit log unavailable"))
	err := credAuditCmd.RunE(credAuditCmd, []string{covCredIDCli3})
	if err == nil || !strings.Contains(err.Error(), "audit log unavailable") {
		t.Fatalf("expected 500, got %v", err)
	}
}

func TestResolveCredentialID_MalformedList(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials", clitest.TextResponse(200, "[broken"))
	if _, err := resolveCredentialID(newAPIClient(), "by-name"); err == nil {
		t.Fatal("expected decode error")
	}
}

// Commands that resolve by name must surface the resolver's failure.
func TestCredGetCmd_ResolveFails(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/credentials", clitest.ErrorResponse(500, "list broke"))
	err := credGetCmd.RunE(credGetCmd, []string{"some-name"})
	if err == nil || !strings.Contains(err.Error(), "list broke") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}
