package main

// Coverage tests for cmd_credential_assignment.go — assign / unassign /
// test RunE paths, including the name→id resolution chains.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const (
	covCredIDCli8  = "ccred01234567890123456789"
	covAgentIDCli8 = "cagent0123456789012345678"
)

// covCredStub registers the credential + agent resolution routes shared
// by assign/unassign tests.
func covCredStub(t *testing.T) *clitest.StubServer {
	t.Helper()
	stub := clitest.NewStubServer()
	t.Cleanup(stub.Close)
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": covCredIDCli8, "name": "anthropic-key"},
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	return stub
}

func TestCredAssignRunE_EnvVarNameRequired(t *testing.T) {
	stub := covCredStub(t)
	_ = stub

	err := credAssignCmd.RunE(credAssignCmd, []string{"anthropic-key", "viktor"})
	if err == nil || !strings.Contains(err.Error(), "--env-var-name is required") {
		t.Errorf("expected env-var-name required; got %v", err)
	}
}

func TestCredAssignRunE_HappyPath(t *testing.T) {
	stub := covCredStub(t)
	stub.OnPost("/api/v1/agents/"+covAgentIDCli8+"/credentials", clitest.JSONResponse(200, map[string]any{"id": "asn-1"}))
	covSetFlagCli8(t, credAssignCmd, "env-var-name", "ANTHROPIC_API_KEY")
	covSetFlagCli8(t, credAssignCmd, "priority", "5")

	if err := credAssignCmd.RunE(credAssignCmd, []string{"anthropic-key", "viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli8+"/credentials")
	if len(calls) != 1 {
		t.Fatalf("expected 1 assign POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["credential_id"] != covCredIDCli8 || body["env_var_name"] != "ANTHROPIC_API_KEY" || body["priority"] != float64(5) {
		t.Errorf("assign body wrong: %v", body)
	}
}

func TestCredAssignRunE_CredentialNotFound(t *testing.T) {
	stub := covCredStub(t)
	_ = stub

	err := credAssignCmd.RunE(credAssignCmd, []string{"ghost-cred", "viktor"})
	if err == nil || !strings.Contains(err.Error(), `credential "ghost-cred" not found`) {
		t.Errorf("expected credential-not-found; got %v", err)
	}
}

func TestCredAssignRunE_APIError(t *testing.T) {
	stub := covCredStub(t)
	stub.OnPost("/api/v1/agents/"+covAgentIDCli8+"/credentials", clitest.ErrorResponse(409, "already assigned"))
	covSetFlagCli8(t, credAssignCmd, "env-var-name", "KEY")

	err := credAssignCmd.RunE(credAssignCmd, []string{"anthropic-key", "viktor"})
	if err == nil || !strings.Contains(err.Error(), "already assigned") {
		t.Errorf("expected conflict; got %v", err)
	}
}

func TestCredUnassignRunE_HappyPath(t *testing.T) {
	stub := covCredStub(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli8+"/credentials", clitest.JSONResponse(200, []map[string]any{
		{"id": "asn-other", "credential_id": "ccredother123456789012345"},
		{"id": "asn-1", "credential_id": covCredIDCli8},
	}))
	stub.OnDelete("/api/v1/agents/"+covAgentIDCli8+"/credentials/asn-1", clitest.EmptyResponse(204))

	if err := credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", "/api/v1/agents/"+covAgentIDCli8+"/credentials/asn-1"); len(calls) != 1 {
		t.Errorf("expected 1 DELETE of asn-1, got %d", len(calls))
	}
}

func TestCredUnassignRunE_NotAssigned(t *testing.T) {
	stub := covCredStub(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli8+"/credentials", clitest.JSONResponse(200, []map[string]any{}))

	err := credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"})
	if err == nil || !strings.Contains(err.Error(), "not assigned to agent") {
		t.Errorf("expected not-assigned; got %v", err)
	}
}

func TestCredTestRunE_ProviderRequired(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	covSetFlagCli8(t, credTestCmd, "value", "sk-123")

	err := credTestCmd.RunE(credTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--provider is required") {
		t.Errorf("expected provider required; got %v", err)
	}
}

func TestCredTestRunE_ValueRequired(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")

	err := credTestCmd.RunE(credTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--value or --value-stdin is required") {
		t.Errorf("expected value required; got %v", err)
	}
}

func TestCredTestRunE_Valid(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": true, "status": 200}))
	covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")
	covSetFlagCli8(t, credTestCmd, "type", "API_KEY")
	covSetFlagCli8(t, credTestCmd, "value", "sk-test-123")

	if err := credTestCmd.RunE(credTestCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/credentials/test")
	if len(calls) != 1 {
		t.Fatalf("expected 1 test POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["provider"] != "ANTHROPIC" || body["value"] != "sk-test-123" || body["type"] != "API_KEY" {
		t.Errorf("test body wrong: %v", body)
	}
}

func TestCredTestRunE_Invalid(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{
		"valid": false, "status": 401, "error": "key revoked",
	}))
	covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")
	covSetFlagCli8(t, credTestCmd, "value", "sk-bad")

	err := credTestCmd.RunE(credTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "credential invalid: key revoked") {
		t.Errorf("expected invalid-with-reason; got %v", err)
	}
}

func TestCredTestRunE_InvalidNoMessage(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": false}))
	covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")
	covSetFlagCli8(t, credTestCmd, "value", "sk-bad")

	err := credTestCmd.RunE(credTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("expected generic validation-failed; got %v", err)
	}
}

// TestCredAssignmentRunE_ErrorBranches sweeps the remaining auth /
// workspace / transport / decode branches across assign/unassign/test.
func TestCredAssignmentRunE_ErrorBranches(t *testing.T) {
	credsOK := clitest.JSONResponse(200, []map[string]any{{"id": covCredIDCli8, "name": "anthropic-key"}})
	agentsOK := clitest.JSONResponse(200, []map[string]any{{"id": covAgentIDCli8, "slug": "viktor"}})
	listPath := "/api/v1/agents/" + covAgentIDCli8 + "/credentials"
	withTestFlags := func(t *testing.T) {
		covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")
		covSetFlagCli8(t, credTestCmd, "value", "sk-x")
	}

	cases := []struct {
		name    string
		run     func() error
		route   func(*clitest.StubServer)
		noAuth  bool
		noWS    bool
		prepare func(*testing.T)
	}{
		{name: "assign no auth", run: func() error { return credAssignCmd.RunE(credAssignCmd, []string{"c", "a"}) }, noAuth: true},
		{name: "assign no workspace", run: func() error { return credAssignCmd.RunE(credAssignCmd, []string{"c", "a"}) }, noWS: true},
		{name: "unassign no auth", run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"c", "a"}) }, noAuth: true},
		{name: "unassign no workspace", run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"c", "a"}) }, noWS: true},
		{name: "test no auth", run: func() error { return credTestCmd.RunE(credTestCmd, nil) }, noAuth: true},
		{name: "assign cred resolve transport",
			run:   func() error { return credAssignCmd.RunE(credAssignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) { s.OnGet("/api/v1/credentials", covAbort()) }},
		{name: "assign post transport",
			prepare: func(t *testing.T) { covSetFlagCli8(t, credAssignCmd, "env-var-name", "KEY") },
			run:     func() error { return credAssignCmd.RunE(credAssignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnPost(listPath, covAbort())
			}},
		{name: "unassign cred resolve transport",
			run:   func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) { s.OnGet("/api/v1/credentials", covAbort()) }},
		{name: "unassign agent resolve transport",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", covAbort())
			}},
		{name: "unassign list transport",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnGet(listPath, covAbort())
			}},
		{name: "unassign list api error",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnGet(listPath, clitest.ErrorResponse(500, "boom"))
			}},
		{name: "unassign list decode",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnGet(listPath, covNotJSON())
			}},
		{name: "unassign delete transport",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnGet(listPath, clitest.JSONResponse(200, []map[string]any{
					{"id": "asn-1", "credential_id": covCredIDCli8},
				}))
				s.OnDelete(listPath+"/asn-1", covAbort())
			}},
		{name: "unassign delete api error",
			run: func() error { return credUnassignCmd.RunE(credUnassignCmd, []string{"anthropic-key", "viktor"}) },
			route: func(s *clitest.StubServer) {
				s.OnGet("/api/v1/credentials", credsOK)
				s.OnGet("/api/v1/agents", agentsOK)
				s.OnGet(listPath, clitest.JSONResponse(200, []map[string]any{
					{"id": "asn-1", "credential_id": covCredIDCli8},
				}))
				s.OnDelete(listPath+"/asn-1", clitest.ErrorResponse(500, "boom"))
			}},
		{name: "test transport", prepare: withTestFlags,
			run:   func() error { return credTestCmd.RunE(credTestCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/credentials/test", covAbort()) }},
		{name: "test api error", prepare: withTestFlags,
			run:   func() error { return credTestCmd.RunE(credTestCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/credentials/test", clitest.ErrorResponse(500, "boom")) }},
		{name: "test decode", prepare: withTestFlags,
			run:   func() error { return credTestCmd.RunE(credTestCmd, nil) },
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/credentials/test", covNotJSON()) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.noAuth {
				cliCfg = &cli.CLIConfig{Server: stub.URL()}
			} else if c.noWS {
				cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
			}
			if c.prepare != nil {
				c.prepare(t)
			}
			if c.route != nil {
				c.route(stub)
			}
			if err := c.run(); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestCredTestRunE_ValueFromStdin(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/credentials/test", clitest.JSONResponse(200, map[string]any{"valid": true}))
	covSetFlagCli8(t, credTestCmd, "provider", "ANTHROPIC")
	covSetFlagCli8(t, credTestCmd, "value-stdin", "true")

	covWithStdinCli8(t, "sk-from-stdin\n", func() {
		if err := credTestCmd.RunE(credTestCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})

	calls := stub.CallsFor("POST", "/api/v1/credentials/test")
	if len(calls) != 1 {
		t.Fatalf("expected 1 test POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["value"] != "sk-from-stdin" {
		t.Errorf("stdin value not used: %v", body)
	}
}
