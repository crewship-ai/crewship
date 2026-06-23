package main

// Error-branch mop-up for cmd_skill.go: auth/workspace gates, API failures,
// malformed JSON decode paths, and transport errors against a dead server.
// Complements cmd_skill_cov_test.go (which hosts the shared cov* helpers).

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// covDeadServer returns the URL of a stub server that has already been
// closed — connections to it fail at the transport level, which is the
// only way to drive the `client.Get/Post returned err` branches.
func covDeadServer(t *testing.T) string {
	t.Helper()
	s := clitest.NewStubServer()
	url := s.URL()
	s.Close()
	return url
}

// covSetupDead points cliCfg at a dead server.
func covSetupDead(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok", Server: covDeadServer(t), Workspace: covWorkspaceIDCli1}
}

// covAuthGates drives a command's RunE through the two entry gates that
// every workspace-scoped command must enforce.
func covAuthGates(t *testing.T, cmd *cobra.Command, args []string, needsWorkspace bool) {
	t.Helper()
	t.Run("no auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		err := cmd.RunE(cmd, args)
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: want not-logged-in; got %v", cmd.Name(), err)
		}
	})
	if !needsWorkspace {
		return
	}
	t.Run("no workspace", func(t *testing.T) {
		saveCLIState(t)
		t.Setenv("CREWSHIP_WORKSPACE", "")
		flagWorkspace = ""
		cliCfg = &cli.CLIConfig{Token: "tok"}
		err := cmd.RunE(cmd, args)
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: want workspace error; got %v", cmd.Name(), err)
		}
	})
}

func TestSkillCmdAuthGatesCov2(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"get", skillGetCmd, []string{"pdf-tools"}},
		{"import", skillImportCmd, nil},
		{"assign", skillAssignCmd, []string{"pdf-tools", "viktor"}},
		{"unassign", skillUnassignCmd, []string{"pdf-tools", "viktor"}},
		{"create", skillCreateCmd, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covAuthGates(t, tc.cmd, tc.args, true)
		})
	}
}

func TestSkillListRunECov2_TransportAndDecodeErrors(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := skillListCmd.RunE(skillListCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills", clitest.ErrorResponse(500, "skills broke"))
		err := skillListCmd.RunE(skillListCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "skills broke") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills", clitest.TextResponse(200, "not json"))
		if err := skillListCmd.RunE(skillListCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestSkillGetRunECov2_ErrorBranches(t *testing.T) {
	t.Run("resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills", clitest.ErrorResponse(500, "resolve broke"))
		err := skillGetCmd.RunE(skillGetCmd, []string{"pdf-tools"})
		if err == nil || !strings.Contains(err.Error(), "resolve broke") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("detail transport error", func(t *testing.T) {
		covSetupDead(t)
		// CUID arg short-circuits resolution; the detail GET then fails.
		if err := skillGetCmd.RunE(skillGetCmd, []string{covSkillID}); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("detail bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills/"+covSkillID, clitest.TextResponse(200, "nope"))
		if err := skillGetCmd.RunE(skillGetCmd, []string{covSkillID}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestSkillImportRunECov2_ErrorBranches(t *testing.T) {
	importPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/import"
	bulkPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/bulk-import"

	t.Run("url transport error", func(t *testing.T) {
		covSetupDead(t)
		if err := skillImportCmd.RunE(skillImportCmd, []string{"https://x/SKILL.md"}); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("url api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(importPath, clitest.ErrorResponse(422, "bad frontmatter"))
		err := skillImportCmd.RunE(skillImportCmd, []string{"https://x/SKILL.md"})
		if err == nil || !strings.Contains(err.Error(), "bad frontmatter") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("url bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(importPath, clitest.TextResponse(200, "nope"))
		if err := skillImportCmd.RunE(skillImportCmd, []string{"https://x/SKILL.md"}); err == nil {
			t.Error("want decode error; got nil")
		}
	})
	t.Run("repo transport error", func(t *testing.T) {
		covSetupDead(t)
		covSetFlag(t, skillImportCmd, "repo", "https://github.com/a/b")
		if err := skillImportCmd.RunE(skillImportCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("repo api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(bulkPath, clitest.ErrorResponse(403, "license gate"))
		covSetFlag(t, skillImportCmd, "repo", "https://github.com/a/b")
		err := skillImportCmd.RunE(skillImportCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "license gate") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("repo bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(bulkPath, clitest.TextResponse(200, "nope"))
		covSetFlag(t, skillImportCmd, "repo", "https://github.com/a/b")
		if err := skillImportCmd.RunE(skillImportCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestSkillAssignRunECov2_ErrorBranches(t *testing.T) {
	t.Run("skill resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills", clitest.ErrorResponse(500, "boom"))
		err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools", "viktor"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("positional agent resolve fails", func(t *testing.T) {
		s := covSetup(t)
		covStubSkillAndAgents(s)
		s.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
		err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools", "viktor"})
		if err == nil || !strings.Contains(err.Error(), "agents broke") {
			t.Errorf("want agent resolve error; got %v", err)
		}
	})
	t.Run("to-agents resolve fails wraps slug", func(t *testing.T) {
		s := covSetup(t)
		covStubSkillAndAgents(s)
		covSetFlag(t, skillAssignCmd, "to-agents", "viktor,ghost")
		err := skillAssignCmd.RunE(skillAssignCmd, []string{"pdf-tools"})
		if err == nil || !strings.Contains(err.Error(), `resolve "ghost"`) {
			t.Errorf("want wrapped resolve error; got %v", err)
		}
	})
	t.Run("fanout transport error", func(t *testing.T) {
		covSetupDead(t)
		// CUID skill + CUID agent: zero resolution calls, so the only
		// network hit is the fan-out POST, which dies at transport level.
		err := skillAssignCmd.RunE(skillAssignCmd, []string{covSkillID, covAgentIDCli1})
		if err == nil || !strings.Contains(err.Error(), "assign failed for 1 of 1") {
			t.Errorf("want fanout failure; got %v", err)
		}
	})
}

func TestSkillUnassignRunECov2_ErrorBranches(t *testing.T) {
	t.Run("skill resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/skills", clitest.ErrorResponse(500, "boom"))
		err := skillUnassignCmd.RunE(skillUnassignCmd, []string{"pdf-tools", "viktor"})
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Errorf("want resolve error; got %v", err)
		}
	})
	t.Run("no target", func(t *testing.T) {
		s := covSetup(t)
		covStubSkillAndAgents(s)
		err := skillUnassignCmd.RunE(skillUnassignCmd, []string{"pdf-tools"})
		if err == nil || !strings.Contains(err.Error(), "specify an agent") {
			t.Errorf("want target error; got %v", err)
		}
	})
}

func TestResolveCrewMembersCov2_ErrorBranches(t *testing.T) {
	t.Run("crew resolve fails", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/crews", clitest.ErrorResponse(500, "crews broke"))
		_, err := resolveCrewMembers(newAPIClient(), "engineering")
		if err == nil || !strings.Contains(err.Error(), "crews broke") {
			t.Errorf("want crew resolve error; got %v", err)
		}
	})
	t.Run("agents api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": covCrewID, "slug": "engineering"},
		}))
		s.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
		_, err := resolveCrewMembers(newAPIClient(), "engineering")
		if err == nil || !strings.Contains(err.Error(), "agents broke") {
			t.Errorf("want agents error; got %v", err)
		}
	})
	t.Run("agents bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
			{"id": covCrewID, "slug": "engineering"},
		}))
		s.OnGet("/api/v1/agents", clitest.TextResponse(200, "nope"))
		_, err := resolveCrewMembers(newAPIClient(), "engineering")
		if err == nil || !strings.Contains(err.Error(), "decode agents") {
			t.Errorf("want decode error; got %v", err)
		}
	})
}

func TestResolveSkillIDCov2_BadJSON(t *testing.T) {
	s := covSetup(t)
	s.OnGet("/api/v1/skills", clitest.TextResponse(200, "nope"))
	if _, err := resolveSkillID(newAPIClient(), "pdf-tools"); err == nil {
		t.Error("want decode error; got nil")
	}
}

func TestResolveSkillIDCov2_TransportError(t *testing.T) {
	covSetupDead(t)
	_, err := resolveSkillID(newAPIClient(), "pdf-tools")
	if err == nil || !strings.Contains(err.Error(), "resolve skill") {
		t.Errorf("want wrapped transport error; got %v", err)
	}
}

func TestSkillCreateRunECov2_ErrorBranches(t *testing.T) {
	genPath := "/api/v1/workspaces/" + covWorkspaceIDCli1 + "/skills/generate"

	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		covSetFlag(t, skillCreateCmd, "slug", "x")
		covSetFlag(t, skillCreateCmd, "prompt", "y")
		if err := skillCreateCmd.RunE(skillCreateCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(genPath, clitest.ErrorResponse(402, "no anthropic credential"))
		covSetFlag(t, skillCreateCmd, "slug", "x")
		covSetFlag(t, skillCreateCmd, "prompt", "y")
		err := skillCreateCmd.RunE(skillCreateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "no anthropic credential") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPost(genPath, clitest.TextResponse(200, "nope"))
		covSetFlag(t, skillCreateCmd, "slug", "x")
		covSetFlag(t, skillCreateCmd, "prompt", "y")
		if err := skillCreateCmd.RunE(skillCreateCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}
