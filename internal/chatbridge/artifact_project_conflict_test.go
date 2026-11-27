package chatbridge

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// An artifact project is never started from a message that carries a Sheet or a workbook file: an
// owner sent a workbook with the project button still selected, and the agent was told to build an
// HTML project out of it. Other attachments and plain messages are unaffected.
func TestArtifactProjectConflictsWithASheetOrAWorkbook(t *testing.T) {
	project := &artifacts.ProjectIntent{}
	block := func(path string) string {
		return "co to je\n\nI've attached a file to this message. The path is relative to your working directory:\n\n- " + path
	}
	for _, tc := range []struct {
		name    string
		intent  *artifacts.ProjectIntent
		refs    bool
		content string
		want    bool
	}{
		{"sheet attached", project, true, "change it", true},
		{"xlsx", project, false, block("attachments/2026-11-26/Loan_plan.xlsx"), true},
		{"xlsm upper", project, false, block("attachments/a/Budget.XLSM"), true},
		{"png", project, false, block("attachments/a/mock.png"), false},
		{"plain", project, false, "a three-screen pricing site", false},
		{"no project", nil, true, block("attachments/a/x.xlsx"), false},
		{"xlsx in text only", project, false, "make a site about report.xlsx", false},
	} {
		if got := artifactProjectConflicts(tc.intent, tc.refs, tc.content); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}
