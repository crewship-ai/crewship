package main

// Error-branch mop-up for cmd_issue_extra.go: auth gates, fetchIssue
// failures, and per-subcommand API/decode error paths. Helpers in
// cmd_skill_cov_test.go / cov2.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

func TestIssueExtraAuthGatesCov2(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"comments", issueCommentsCmd, []string{covIssueIdent}},
		{"relate", issueRelateCmd, []string{covIssueIdent, "ENG-43"}},
		{"relations", issueRelationsCmd, []string{covIssueIdent}},
		{"unrelate", issueUnrelateCmd, []string{"rel-1"}},
		{"bind-routine", issueBindRoutineCmd, []string{covIssueIdent, "nightly"}},
		{"unbind-routine", issueUnbindRoutineCmd, []string{covIssueIdent}},
		{"subtasks", issueSubtasksCmd, []string{covIssueIdent}},
		{"activity", issueActivityCmd, []string{covIssueIdent}},
		{"bulk update", issueBulkUpdateCmd, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			covAuthGates(t, tc.cmd, tc.args, true)
		})
	}
}

// Every read/mutate subcommand funnels through fetchIssue first; a 404 on
// the issue lookup must short-circuit each of them with the same error.
func TestIssueExtraFetchIssueFailsCov2(t *testing.T) {
	cases := []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"comments", issueCommentsCmd, []string{covIssueIdent}},
		{"relate", issueRelateCmd, []string{covIssueIdent, "ENG-43"}},
		{"relations", issueRelationsCmd, []string{covIssueIdent}},
		{"bind-routine", issueBindRoutineCmd, []string{covIssueIdent, "nightly"}},
		{"unbind-routine", issueUnbindRoutineCmd, []string{covIssueIdent}},
		{"subtasks", issueSubtasksCmd, []string{covIssueIdent}},
		{"activity", issueActivityCmd, []string{covIssueIdent}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := covSetup(t)
			s.OnGet("/api/v1/issues/"+covIssueIdent, clitest.ErrorResponse(404, "Issue not found"))
			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "Issue not found") {
				t.Errorf("want fetchIssue 404 surfaced; got %v", err)
			}
		})
	}
}

// The sub-resource fetch after a successful issue lookup can fail too —
// each command must surface the API error rather than swallow it.
func TestIssueExtraSubResourceErrorsCov2(t *testing.T) {
	base := "/api/v1/crews/" + covCrewID + "/issues/" + covIssueIdent
	cases := []struct {
		name   string
		cmd    *cobra.Command
		args   []string
		method string
		route  string
	}{
		{"comments", issueCommentsCmd, []string{covIssueIdent}, "GET", base + "/comments"},
		{"relate", issueRelateCmd, []string{covIssueIdent, "ENG-43"}, "POST", base + "/relations"},
		{"relations", issueRelationsCmd, []string{covIssueIdent}, "GET", base + "/relations"},
		{"unbind-routine", issueUnbindRoutineCmd, []string{covIssueIdent}, "PATCH", base},
		{"subtasks", issueSubtasksCmd, []string{covIssueIdent}, "GET", base + "/subtasks"},
		{"activity", issueActivityCmd, []string{covIssueIdent}, "GET", base + "/activity"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" api error", func(t *testing.T) {
			s := covSetup(t)
			covStubIssue(s)
			s.On(tc.method, tc.route, clitest.ErrorResponse(500, "subresource broke"))
			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "subresource broke") {
				t.Errorf("want API error; got %v", err)
			}
		})
	}

	// Decode failures for the GET-list commands.
	decodeCases := []struct {
		name  string
		cmd   *cobra.Command
		route string
	}{
		{"comments", issueCommentsCmd, base + "/comments"},
		{"relations", issueRelationsCmd, base + "/relations"},
		{"subtasks", issueSubtasksCmd, base + "/subtasks"},
		{"activity", issueActivityCmd, base + "/activity"},
	}
	for _, tc := range decodeCases {
		t.Run(tc.name+" bad json", func(t *testing.T) {
			s := covSetup(t)
			covStubIssue(s)
			s.OnGet(tc.route, clitest.TextResponse(200, "nope"))
			if err := tc.cmd.RunE(tc.cmd, []string{covIssueIdent}); err == nil {
				t.Error("want decode error; got nil")
			}
		})
	}
}

func TestIssueUnrelateRunECov2_DeleteFails(t *testing.T) {
	s := covSetup(t)
	s.OnDelete("/api/v1/relations/rel-1", clitest.ErrorResponse(404, "relation gone"))
	err := issueUnrelateCmd.RunE(issueUnrelateCmd, []string{"rel-1"})
	if err == nil || !strings.Contains(err.Error(), "relation gone") {
		t.Errorf("want delete error; got %v", err)
	}
}

func TestIssueBindRoutineRunECov2_RoutineResolveFails(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli1+"/pipelines/ghost",
		clitest.ErrorResponse(404, "pipeline missing"))
	err := issueBindRoutineCmd.RunE(issueBindRoutineCmd, []string{covIssueIdent, "ghost"})
	if err == nil || !strings.Contains(err.Error(), "pipeline missing") {
		t.Errorf("want routine resolve error; got %v", err)
	}
}

func TestIssueBindRoutineRunECov2_PatchFails(t *testing.T) {
	s := covSetup(t)
	covStubIssue(s)
	s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli1+"/pipelines/nightly",
		clitest.JSONResponse(200, map[string]string{"id": "cpipe0123456789abcdefghi"}))
	s.OnPatch("/api/v1/crews/"+covCrewID+"/issues/"+covIssueIdent,
		clitest.ErrorResponse(500, "patch broke"))
	err := issueBindRoutineCmd.RunE(issueBindRoutineCmd, []string{covIssueIdent, "nightly"})
	if err == nil || !strings.Contains(err.Error(), "patch broke") {
		t.Errorf("want patch error; got %v", err)
	}
}

func TestResolveRoutineIDCov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		if _, err := resolveRoutineID(newAPIClient(), "nightly"); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli1+"/pipelines/nightly",
			clitest.TextResponse(200, "nope"))
		if _, err := resolveRoutineID(newAPIClient(), "nightly"); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}

func TestIssueBulkUpdateRunECov2_ErrorBranches(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		covSetupDead(t)
		covSetFlag(t, issueBulkUpdateCmd, "ids", "ms_a")
		covSetFlag(t, issueBulkUpdateCmd, "status", "DONE")
		if err := issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil); err == nil {
			t.Error("want transport error; got nil")
		}
	})
	t.Run("api error", func(t *testing.T) {
		s := covSetup(t)
		s.OnPatch("/api/v1/issues/bulk", clitest.ErrorResponse(400, "unknown status"))
		covSetFlag(t, issueBulkUpdateCmd, "ids", "ms_a")
		covSetFlag(t, issueBulkUpdateCmd, "status", "NOPE")
		err := issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown status") {
			t.Errorf("want API error; got %v", err)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		s := covSetup(t)
		s.OnPatch("/api/v1/issues/bulk", clitest.TextResponse(200, "nope"))
		covSetFlag(t, issueBulkUpdateCmd, "ids", "ms_a")
		covSetFlag(t, issueBulkUpdateCmd, "status", "DONE")
		if err := issueBulkUpdateCmd.RunE(issueBulkUpdateCmd, nil); err == nil {
			t.Error("want decode error; got nil")
		}
	})
}
