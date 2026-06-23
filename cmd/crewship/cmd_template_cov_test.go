package main

// Coverage tests for cmd_template.go — list / get / deploy RunE paths.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestTemplateListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := templateListCmd.RunE(templateListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestTemplateListRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	longDesc := strings.Repeat("d", 60)
	stub.OnGet("/api/v1/crew-templates", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "ctpl0123456789012345678", "name": "Backend Team", "slug": "backend-team",
			"description": longDesc, "category": "engineering", "is_builtin": true,
			"agents": []map[string]string{{"name": "Viktor"}, {"name": "Eva"}},
		},
		{
			"id": "ctpl1123456789012345678", "name": "Empty", "slug": "empty",
			"category": "misc", "is_builtin": false,
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := templateListCmd.RunE(templateListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"backend-team", "engineering", "yes", "no", "..."} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestTemplateGetRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/crew-templates/backend-team", clitest.JSONResponse(200, map[string]any{
		"id": "ctpl0123456789012345678", "name": "Backend Team", "slug": "backend-team",
		"description": "ships code", "category": "engineering", "is_builtin": true,
		"created_at": "2026-06-01T00:00:00Z",
		"agents": []map[string]any{
			{"name": "Viktor", "slug": "viktor", "role_title": "Lead Engineer", "agent_role": "LEAD"},
			{"name": "Eva", "slug": "eva", "agent_role": "AGENT"},
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := templateGetCmd.RunE(templateGetCmd, []string{"backend-team"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Backend Team", "ships code", "AGENTS (2)", "Lead Engineer", "viktor", "LEAD"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q:\n%s", want, out)
		}
	}
}

func TestTemplateGetRunE_NotFound(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/crew-templates/ghost", clitest.ErrorResponse(404, "Template not found"))

	err := templateGetCmd.RunE(templateGetCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "Template not found") {
		t.Errorf("expected not-found; got %v", err)
	}
}

func TestTemplateDeployRunE_NameRequired(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	err := templateDeployCmd.RunE(templateDeployCmd, []string{"backend-team"})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name required; got %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("no API calls expected; got %d", len(stub.Calls()))
	}
}

func TestTemplateDeployRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/crew-templates/backend-team/deploy", clitest.JSONResponse(200, map[string]any{
		"crew_id": "ccrew0123456789012345678", "crew_name": "My Backend", "crew_slug": "my-backend",
		"agent_count": 3, "agent_ids": []string{"a1", "a2", "a3"},
	}))
	covSetFlagCli8(t, templateDeployCmd, "name", "My Backend")
	covSetFlagCli8(t, templateDeployCmd, "slug", "my-backend")

	if err := templateDeployCmd.RunE(templateDeployCmd, []string{"backend-team"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/crew-templates/backend-team/deploy")
	if len(calls) != 1 {
		t.Fatalf("expected 1 deploy POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["crew_name"] != "My Backend" || body["crew_slug"] != "my-backend" {
		t.Errorf("deploy body wrong: %v", body)
	}
}

// TestTemplateRunE_ErrorBranches sweeps the remaining auth / workspace /
// transport / decode branches.
func TestTemplateRunE_ErrorBranches(t *testing.T) {
	withName := func(t *testing.T) { covSetFlagCli8(t, templateDeployCmd, "name", "x") }
	cases := []struct {
		name    string
		cmd     *cobra.Command
		args    []string
		route   func(*clitest.StubServer)
		noAuth  bool
		noWS    bool
		prepare func(*testing.T)
	}{
		{name: "list no workspace", cmd: templateListCmd, noWS: true},
		{name: "get no auth", cmd: templateGetCmd, args: []string{"x"}, noAuth: true},
		{name: "get no workspace", cmd: templateGetCmd, args: []string{"x"}, noWS: true},
		{name: "deploy no auth", cmd: templateDeployCmd, args: []string{"x"}, noAuth: true},
		{name: "deploy no workspace", cmd: templateDeployCmd, args: []string{"x"}, noWS: true},
		{name: "list transport", cmd: templateListCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/crew-templates", covAbort())
		}},
		{name: "list api error", cmd: templateListCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/crew-templates", clitest.ErrorResponse(500, "boom"))
		}},
		{name: "list decode", cmd: templateListCmd, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/crew-templates", covNotJSON())
		}},
		{name: "get transport", cmd: templateGetCmd, args: []string{"x"}, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/crew-templates/x", covAbort())
		}},
		{name: "get decode", cmd: templateGetCmd, args: []string{"x"}, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/crew-templates/x", covNotJSON())
		}},
		{name: "deploy transport", cmd: templateDeployCmd, args: []string{"x"}, prepare: withName,
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/crew-templates/x/deploy", covAbort()) }},
		{name: "deploy decode", cmd: templateDeployCmd, args: []string{"x"}, prepare: withName,
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/crew-templates/x/deploy", covNotJSON()) }},
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
			if err := c.cmd.RunE(c.cmd, c.args); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestTemplateDeployRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/crew-templates/backend-team/deploy", clitest.ErrorResponse(409, "crew slug already exists"))
	covSetFlagCli8(t, templateDeployCmd, "name", "My Backend")

	err := templateDeployCmd.RunE(templateDeployCmd, []string{"backend-team"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected conflict; got %v", err)
	}
}
