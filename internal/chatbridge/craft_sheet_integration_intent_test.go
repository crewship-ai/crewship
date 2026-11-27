package chatbridge

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// integration: an artifact project never starts from a message that carries a Sheet (an attachment chip) or
// a workbook file (an "- name.xlsx" line of the attachment block); a message with neither, or a
// workbook only named in prose, is not a conflict, and without a project nothing conflicts.
func TestSheetITConflictingIntentOnlyForAProjectWithASheetOrWorkbook(t *testing.T) {
	project := &artifacts.ProjectIntent{Kind: "artifact"}
	for _, tc := range []struct {
		name     string
		intent   *artifacts.ProjectIntent
		attached bool
		content  string
		conflict bool
	}{
		{"project with a Sheet chip", project, true, "make a site", true},
		{"project with an xlsx line", project, false, "make a site\n\n- /work/files/Rozpočet 2026.xlsx", true},
		{"project with an xlsm line, upper case", project, false, "make a site\n- /work/files/MAKRA.XLSM  ", true},
		{"project, workbook named in prose", project, false, "make a site like budget.xlsx but nicer", false},
		{"project, csv attached", project, false, "make a site\n- /work/files/data.csv", false},
		{"project alone", project, false, "make a site", false},
		{"no project, Sheet chip", nil, true, "- /work/files/a.xlsx", false},
	} {
		if got := artifactProjectConflicts(tc.intent, tc.attached, tc.content); got != tc.conflict {
			t.Errorf("%s: conflict=%v want %v", tc.name, got, tc.conflict)
		}
	}
	if !errors.Is(ErrConflictingIntent, ErrConflictingIntent) || ErrConflictingIntent.Error() == "" {
		t.Fatal("ErrConflictingIntent")
	}
}
