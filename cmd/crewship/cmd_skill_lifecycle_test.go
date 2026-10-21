package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestSkillArchiveRestoreRunE(t *testing.T) {
	tests := []struct {
		name   string
		cmd    *cobra.Command
		action string
		status int
		errHas string
	}{
		{"archive", skillArchiveCmd, "archive", 200, ""},
		{"restore", skillRestoreCmd, "restore", 200, ""},
		{"archive refused for a deprecated skill", skillArchiveCmd, "archive", 409, "deprecated"},
		{"restore of an unknown skill", skillRestoreCmd, "restore", 404, "skill not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := covSetupCli5(t)
			path := "/api/v1/workspaces/" + covWSCli5 + "/skills/" + covSkillIDCli5 + "/" + tc.action
			if tc.status == 200 {
				stub.OnPost(path, clitest.JSONResponse(200, map[string]string{"skill_id": covSkillIDCli5}))
			} else {
				stub.OnPost(path, clitest.ErrorResponse(tc.status, tc.errHas))
			}
			err := tc.cmd.RunE(tc.cmd, []string{covSkillIDCli5})
			if tc.errHas == "" && err != nil {
				t.Fatalf("RunE: %v", err)
			}
			if tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.errHas)
			}
			if calls := stub.CallsFor("POST", path); len(calls) != 1 {
				t.Errorf("POST %s called %d times, want 1", path, len(calls))
			}
		})
	}
}
