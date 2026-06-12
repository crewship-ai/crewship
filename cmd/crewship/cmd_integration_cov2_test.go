package main

// Error-branch mop-up for cmd_integration.go: auth/workspace gates plus the
// API-error, decode-error, and transport-error paths of every workspace-level
// integration subcommand. Helpers in cmd_skill_cov_test.go / cov2.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestIntegrationCmdAuthGatesCov2(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"list", intgListCmd, nil},
		{"add", intgAddCmd, nil},
		{"remove", intgRemoveCmd, []string{"gmail"}},
		{"bind", intgBindCmd, nil},
		{"unbind", intgUnbindCmd, nil},
		{"agent-bindings", intgAgentListCmd, []string{"pepa"}},
		{"resolve", intgResolveCmd, []string{"pepa"}},
		{"get", intgGetCmd, []string{"gmail"}},
		{"test", intgTestCmd, []string{"gmail"}},
		{"crews-overview", intgCrewsOverviewCmd, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covAuthGates(t, tc.cmd, tc.args, true)
		})
	}
}

func TestToggleIntegrationCov2_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := toggleIntegration("gmail", true)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("want workspace error; got %v", err)
	}
}

func TestToggleIntegrationCov2_PatchFails(t *testing.T) {
	s := covSetup(t)
	covStubIntegrationList(s)
	s.OnPatch("/api/v1/integrations/"+covIntgID, clitest.ErrorResponse(500, "patch broke"))
	err := toggleIntegration("gmail", false)
	if err == nil || !strings.Contains(err.Error(), "patch broke") {
		t.Errorf("want patch error; got %v", err)
	}
}

func TestIntgListRunECov2_TransportAndDecode(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := intgListCmd.RunE(intgListCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/integrations", clitest.TextResponse(200, "nope"))
		if err := intgListCmd.RunE(intgListCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestIntgAddRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		covSetFlag(t, intgAddCmd, "name", "gmail")
		if err := intgAddCmd.RunE(intgAddCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost("/api/v1/integrations", clitest.ErrorResponse(409, "name taken"))
		covSetFlag(t, intgAddCmd, "name", "gmail")
		err := intgAddCmd.RunE(intgAddCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "name taken") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost("/api/v1/integrations", clitest.TextResponse(201, "nope"))
		covSetFlag(t, intgAddCmd, "name", "gmail")
		err := intgAddCmd.RunE(intgAddCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Errorf("want decode error; got %v", err)
		}
	})
}

func TestIntgRemoveRunECov2_ErrorBranches(t *testing.T) {
	t.Run("aborted without --yes", func(t *testing.T) {
		covSetup(t)
		// Test binaries run without a TTY; the plain-stdin fallback reads
		// EOF, which must abort rather than delete.
		err := intgRemoveCmd.RunE(intgRemoveCmd, []string{"gmail"})
		if err == nil || !strings.Contains(err.Error(), "aborted") {
			t.Errorf("want abort; got %v", err)
		}
	})
	t.Run("resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/integrations", clitest.ErrorResponse(500, "list broke"))
		covSetFlag(t, intgRemoveCmd, "yes", "true")
		err := intgRemoveCmd.RunE(intgRemoveCmd, []string{"gmail"})
		if err == nil || !strings.Contains(err.Error(), "list broke") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("delete fails", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnDelete("/api/v1/integrations/"+covIntgID, clitest.ErrorResponse(409, "still bound"))
		covSetFlag(t, intgRemoveCmd, "yes", "true")
		err := intgRemoveCmd.RunE(intgRemoveCmd, []string{"gmail"})
		if err == nil || !strings.Contains(err.Error(), "still bound") {
			t.Errorf("want delete error; got %v", err)
		}
	})
}

func TestIntgBindRunECov2_ErrorBranches(t *testing.T) {
	t.Run("agent resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
		covSetFlag(t, intgBindCmd, "agent", "ghost")
		covSetFlag(t, intgBindCmd, "server", "gmail")
		err := intgBindCmd.RunE(intgBindCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "agent not found") {
			t.Errorf("want agent resolve error; got %v", err)
		}
	})
	t.Run("server resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/integrations", clitest.JSONResponse(200, []map[string]string{}))
		covSetFlag(t, intgBindCmd, "agent", "pepa")
		covSetFlag(t, intgBindCmd, "server", "ghost")
		err := intgBindCmd.RunE(intgBindCmd, nil)
		if err == nil || !strings.Contains(err.Error(), `integration "ghost" not found`) {
			t.Errorf("want server resolve error; got %v", err)
		}
	})
	t.Run("credential resolve fails", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]string{}))
		covSetFlag(t, intgBindCmd, "agent", "pepa")
		covSetFlag(t, intgBindCmd, "server", "gmail")
		covSetFlag(t, intgBindCmd, "credential", "ghost-cred")
		err := intgBindCmd.RunE(intgBindCmd, nil)
		if err == nil || !strings.Contains(err.Error(), `credential "ghost-cred" not found`) {
			t.Errorf("want credential resolve error; got %v", err)
		}
	})
	t.Run("bind post fails", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnPost("/api/v1/agents/"+covAgentIDCli1+"/integrations",
			clitest.ErrorResponse(409, "already bound"))
		covSetFlag(t, intgBindCmd, "agent", "pepa")
		covSetFlag(t, intgBindCmd, "server", "gmail")
		err := intgBindCmd.RunE(intgBindCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "already bound") {
			t.Errorf("want bind error; got %v", err)
		}
	})
}

func TestIntgUnbindRunECov2_DeleteFails(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": covAgentIDCli1, "slug": "pepa"},
	}))
	s.OnDelete("/api/v1/agents/"+covAgentIDCli1+"/integrations/b1",
		clitest.ErrorResponse(404, "binding not found"))
	covSetFlag(t, intgUnbindCmd, "agent", "pepa")
	covSetFlag(t, intgUnbindCmd, "binding-id", "b1")
	err := intgUnbindCmd.RunE(intgUnbindCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "binding not found") {
		t.Errorf("want delete error; got %v", err)
	}
}

func TestIntgAgentBindingsRunECov2_ErrorBranches(t *testing.T) {
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations",
			clitest.ErrorResponse(500, "bindings broke"))
		err := intgAgentListCmd.RunE(intgAgentListCmd, []string{"pepa"})
		if err == nil || !strings.Contains(err.Error(), "bindings broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations",
			clitest.TextResponse(200, "nope"))
		if err := intgAgentListCmd.RunE(intgAgentListCmd, []string{"pepa"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestIntgResolveRunECov2_ErrorBranches(t *testing.T) {
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations/resolved",
			clitest.ErrorResponse(500, "resolve broke"))
		err := intgResolveCmd.RunE(intgResolveCmd, []string{"pepa"})
		if err == nil || !strings.Contains(err.Error(), "resolve broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
			{"id": covAgentIDCli1, "slug": "pepa"},
		}))
		s.OnGet("/api/v1/agents/"+covAgentIDCli1+"/integrations/resolved",
			clitest.TextResponse(200, "nope"))
		if err := intgResolveCmd.RunE(intgResolveCmd, []string{"pepa"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestIntgGetRunECov2_ErrorBranches(t *testing.T) {
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnGet("/api/v1/integrations/"+covIntgID, clitest.ErrorResponse(500, "get broke"))
		err := intgGetCmd.RunE(intgGetCmd, []string{"gmail"})
		if err == nil || !strings.Contains(err.Error(), "get broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnGet("/api/v1/integrations/"+covIntgID, clitest.TextResponse(200, "nope"))
		if err := intgGetCmd.RunE(intgGetCmd, []string{"gmail"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
	t.Run("all pointer fields set", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnGet("/api/v1/integrations/"+covIntgID, clitest.JSONResponse(200, map[string]any{
			"id": covIntgID, "name": "gmail", "display_name": "Google Gmail",
			"transport": "stdio", "endpoint": nil, "command": "npx server",
			"enabled": false, "icon": "mail", "created_at": "a", "updated_at": "b",
		}))
		out, err := covCaptureStdout(t, func() error {
			return intgGetCmd.RunE(intgGetCmd, []string{"gmail"})
		})
		if err != nil {
			t.Fatalf("RunE: %v", err)
		}
		for _, want := range []string{"npx server", "mail", "no"} {
			if !strings.Contains(out, want) {
				t.Errorf("detail missing %q:\n%s", want, out)
			}
		}
	})
}

func TestIntgTestRunECov2_ErrorBranches(t *testing.T) {
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnPost("/api/v1/integrations/"+covIntgID+"/test",
			clitest.ErrorResponse(502, "endpoint unreachable"))
		err := intgTestCmd.RunE(intgTestCmd, []string{"gmail"})
		if err == nil || !strings.Contains(err.Error(), "endpoint unreachable") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		covStubIntegrationList(s)
		s.OnPost("/api/v1/integrations/"+covIntgID+"/test", clitest.TextResponse(200, "nope"))
		if err := intgTestCmd.RunE(intgTestCmd, []string{"gmail"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestIntgCrewsOverviewRunECov2_ErrorBranches(t *testing.T) {
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/integrations/crews", clitest.ErrorResponse(500, "overview broke"))
		err := intgCrewsOverviewCmd.RunE(intgCrewsOverviewCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "overview broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/integrations/crews", clitest.TextResponse(200, "nope"))
		if err := intgCrewsOverviewCmd.RunE(intgCrewsOverviewCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}
