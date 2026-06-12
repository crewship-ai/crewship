package main

// Coverage tests for cmd_milestone.go — list / create / update / delete
// RunE paths against a stub API server.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestMilestoneCmdStructure(t *testing.T) {
	have := map[string]bool{}
	for _, sub := range projectMilestoneCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "create", "update", "delete"} {
		if !have[want] {
			t.Errorf("milestone missing subcommand %q; have %v", want, have)
		}
	}
}

func TestMilestoneListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := projectMilestoneListCmd.RunE(projectMilestoneListCmd, []string{"proj-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestMilestoneListRunE_NoWorkspace(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := projectMilestoneListCmd.RunE(projectMilestoneListCmd, []string{"proj-1"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestMilestoneListRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	target := "2026-07-01"
	stub.OnGet("/api/v1/projects/proj-1/milestones", clitest.JSONResponse(200, []map[string]any{
		{
			"id": "cmilestone012345678901", "project_id": "proj-1", "name": "Beta",
			"status": "active", "position": 1, "issue_count": 4, "done_count": 2,
			"target_date": target,
		},
		{
			"id": "cmilestone112345678901", "project_id": "proj-1", "name": "GA",
			"status": "planned", "position": 2, "issue_count": 0, "done_count": 0,
		},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := projectMilestoneListCmd.RunE(projectMilestoneListCmd, []string{"proj-1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Beta", "GA", "2/4", "2026-07-01", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestMilestoneListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/projects/ghost/milestones", clitest.ErrorResponse(404, "Project not found"))

	err := projectMilestoneListCmd.RunE(projectMilestoneListCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "Project not found") {
		t.Errorf("expected not-found error; got %v", err)
	}
}

func TestMilestoneCreateRunE_NameRequired(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	err := projectMilestoneCreateCmd.RunE(projectMilestoneCreateCmd, []string{"proj-1"})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("expected --name required; got %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("no API calls expected on validation failure; got %d", len(stub.Calls()))
	}
}

func TestMilestoneCreateRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/projects/proj-1/milestones", clitest.JSONResponse(200, map[string]any{
		"id": "cmilestone012345678901", "name": "Beta", "status": "active",
	}))

	covSetFlagCli8(t, projectMilestoneCreateCmd, "name", "Beta")
	covSetFlagCli8(t, projectMilestoneCreateCmd, "description", "first beta")
	covSetFlagCli8(t, projectMilestoneCreateCmd, "target-date", "2026-07-01")
	covSetFlagCli8(t, projectMilestoneCreateCmd, "status", "active")

	if err := projectMilestoneCreateCmd.RunE(projectMilestoneCreateCmd, []string{"proj-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", "/api/v1/projects/proj-1/milestones")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "Beta" || body["description"] != "first beta" ||
		body["target_date"] != "2026-07-01" || body["status"] != "active" {
		t.Errorf("create body wrong: %v", body)
	}
}

func TestMilestoneCreateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPost("/api/v1/projects/proj-1/milestones", clitest.ErrorResponse(409, "milestone name already exists"))
	covSetFlagCli8(t, projectMilestoneCreateCmd, "name", "Beta")

	err := projectMilestoneCreateCmd.RunE(projectMilestoneCreateCmd, []string{"proj-1"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected conflict error; got %v", err)
	}
}

func TestMilestoneUpdateRunE_NoFields(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	err := projectMilestoneUpdateCmd.RunE(projectMilestoneUpdateCmd, []string{"m-1"})
	if err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected no-fields error; got %v", err)
	}
}

func TestMilestoneUpdateRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPatch("/api/v1/milestones/m-1", clitest.JSONResponse(200, map[string]any{"ok": true}))

	covSetFlagCli8(t, projectMilestoneUpdateCmd, "name", "Beta 2")
	covSetFlagCli8(t, projectMilestoneUpdateCmd, "description", "updated")
	covSetFlagCli8(t, projectMilestoneUpdateCmd, "target-date", "2026-08-01")
	covSetFlagCli8(t, projectMilestoneUpdateCmd, "status", "done")
	covSetFlagCli8(t, projectMilestoneUpdateCmd, "position", "3")

	if err := projectMilestoneUpdateCmd.RunE(projectMilestoneUpdateCmd, []string{"m-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("PATCH", "/api/v1/milestones/m-1")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PATCH, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "Beta 2" || body["status"] != "done" || body["position"] != float64(3) {
		t.Errorf("update body wrong: %v", body)
	}
}

func TestMilestoneUpdateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnPatch("/api/v1/milestones/ghost", clitest.ErrorResponse(404, "Milestone not found"))
	covSetFlagCli8(t, projectMilestoneUpdateCmd, "name", "x")

	err := projectMilestoneUpdateCmd.RunE(projectMilestoneUpdateCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), "Milestone not found") {
		t.Errorf("expected not-found error; got %v", err)
	}
}

func TestMilestoneDeleteRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnDelete("/api/v1/milestones/m-1", clitest.EmptyResponse(204))
	covSetFlagCli8(t, projectMilestoneDeleteCmd, "yes", "true")

	if err := projectMilestoneDeleteCmd.RunE(projectMilestoneDeleteCmd, []string{"m-1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", "/api/v1/milestones/m-1"); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}
}

// TestMilestoneRunE_ErrorBranches sweeps auth / workspace / transport /
// decode / API-error branches across the four subcommands.
func TestMilestoneRunE_ErrorBranches(t *testing.T) {
	type tc struct {
		name    string
		cmd     *cobra.Command
		args    []string
		route   func(*clitest.StubServer)
		noAuth  bool
		noWS    bool
		prepare func(t *testing.T)
	}
	withName := func(t *testing.T) { covSetFlagCli8(t, projectMilestoneCreateCmd, "name", "Beta") }
	withUpd := func(t *testing.T) { covSetFlagCli8(t, projectMilestoneUpdateCmd, "name", "x") }
	withYes := func(t *testing.T) { covSetFlagCli8(t, projectMilestoneDeleteCmd, "yes", "true") }
	cases := []tc{
		{name: "create no auth", cmd: projectMilestoneCreateCmd, args: []string{"p"}, noAuth: true},
		{name: "create no workspace", cmd: projectMilestoneCreateCmd, args: []string{"p"}, noWS: true},
		{name: "update no auth", cmd: projectMilestoneUpdateCmd, args: []string{"m"}, noAuth: true},
		{name: "update no workspace", cmd: projectMilestoneUpdateCmd, args: []string{"m"}, noWS: true},
		{name: "delete no auth", cmd: projectMilestoneDeleteCmd, args: []string{"m"}, noAuth: true},
		{name: "delete no workspace", cmd: projectMilestoneDeleteCmd, args: []string{"m"}, noWS: true},
		{name: "list transport", cmd: projectMilestoneListCmd, args: []string{"p"}, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/projects/p/milestones", covAbort())
		}},
		{name: "list decode", cmd: projectMilestoneListCmd, args: []string{"p"}, route: func(s *clitest.StubServer) {
			s.OnGet("/api/v1/projects/p/milestones", covNotJSON())
		}},
		{name: "create transport", cmd: projectMilestoneCreateCmd, args: []string{"p"}, prepare: withName,
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/projects/p/milestones", covAbort()) }},
		{name: "create decode", cmd: projectMilestoneCreateCmd, args: []string{"p"}, prepare: withName,
			route: func(s *clitest.StubServer) { s.OnPost("/api/v1/projects/p/milestones", covNotJSON()) }},
		{name: "update transport", cmd: projectMilestoneUpdateCmd, args: []string{"m"}, prepare: withUpd,
			route: func(s *clitest.StubServer) { s.OnPatch("/api/v1/milestones/m", covAbort()) }},
		{name: "delete transport", cmd: projectMilestoneDeleteCmd, args: []string{"m"}, prepare: withYes,
			route: func(s *clitest.StubServer) { s.OnDelete("/api/v1/milestones/m", covAbort()) }},
		{name: "delete api error", cmd: projectMilestoneDeleteCmd, args: []string{"m"}, prepare: withYes,
			route: func(s *clitest.StubServer) {
				s.OnDelete("/api/v1/milestones/m", clitest.ErrorResponse(404, "Milestone not found"))
			}},
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

func TestMilestoneDeleteRunE_Aborted(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())

	// Non-TTY stdin answering "n" → confirmAction must abort before any
	// API call happens.
	covWithStdinCli8(t, "n\n", func() {
		err := projectMilestoneDeleteCmd.RunE(projectMilestoneDeleteCmd, []string{"m-1"})
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Errorf("expected aborted; got %v", err)
		}
	})
	if len(stub.Calls()) != 0 {
		t.Errorf("no API calls expected after abort; got %d", len(stub.Calls()))
	}
}
