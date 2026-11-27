package chatbridge

import (
	"strings"
	"testing"
)

// A message that attaches a Sheet tells the agent the Sheet is open beside the chat and which
// tools work on it, so it works on that Sheet rather than on the uploaded file's copy.
func TestAttachedSheetRunPromptNamesTheOpenSheetAndItsTools(t *testing.T) {
	content := "smaž všechny loan id\n\nI've attached a file to this message. The path is relative to your working directory:\n\n- attachments/a/Loan_plan.xlsx"
	got := attachedSheetRunPrompt(content, []string{"page_1"})
	for _, want := range []string{content, "@page_1", "open beside the chat", "craft_sheet_read", "craft_sheet_apply", "not the attached file"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if attachedSheetRunPrompt(content, nil) != content {
		t.Error("no Sheet: the message is unchanged")
	}
}

func TestAttachedSheetRunPromptAsksForTheSheetInEveryWrite(t *testing.T) {
	got := attachedSheetRunPrompt("fix it", []string{"p1"})
	if !strings.Contains(got, "Name the sheet in every write with params.sheet (sheet L, cell B8 is 'L'!B8)") {
		t.Fatalf("prompt %q", got)
	}
}
