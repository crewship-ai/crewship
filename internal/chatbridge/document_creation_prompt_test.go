package chatbridge

import (
	"github.com/crewship-ai/crewship/internal/documents"
	"strings"
	"testing"
)

func TestDocumentCreationPromptScopesDraftToolsToOriginalPurpose(t *testing.T) {
	for _, drafting := range []bool{false, true} {
		t.Run(map[bool]string{false: "one shot", true: "original drafting"}[drafting], func(t *testing.T) {
			intent := &documents.CreationIntent{SubmissionID: "original-submission", Drafting: drafting}
			prompt := documentCreationRunPrompt("vytvoř dokument o zahradě", intent)
			if !strings.HasPrefix(prompt, "vytvoř dokument o zahradě\n") || !strings.Contains(prompt, `"mutation_id":"original-submission"`) {
				t.Fatal("lost original human request or stable creation identity")
			}
			if strings.Contains(prompt, "doc_draft_apply") != drafting || strings.Contains(prompt, "doc draft prepare") != drafting {
				t.Fatal("drafting guidance does not match the original admitted purpose")
			}
			if !strings.Contains(prompt, "Do not upload document contents to crew Files.") {
				t.Fatal("lost private-content boundary")
			}
			if drafting && (!strings.Contains(prompt, "same live run") || !strings.Contains(prompt, "checkpoint") || !strings.Contains(prompt, "new run")) {
				t.Fatal("missing draft lifetime, checkpoint or recovery boundary")
			}
		})
	}
}
