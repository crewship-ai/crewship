package chatbridge

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// A successful design_apply already retains a version and answers the
// comment; a design_snapshot afterwards only adds a duplicate version.
func TestArtifactProjectRunPromptDesignDoesNotAskForSnapshot(t *testing.T) {
	prompt := artifactProjectRunPrompt("Shorten the title", &artifacts.ProjectIntent{Kind: "design", PageID: "page-1", BaseVersion: 2, CommentID: "comment-1"})
	if !strings.Contains(prompt, "design_apply") {
		t.Fatalf("design prompt lost design_apply: %q", prompt)
	}
	if !strings.Contains(prompt, "do not call design_snapshot") {
		t.Fatalf("design prompt must forbid a follow-up snapshot: %q", prompt)
	}
	if strings.Contains(prompt, "then design_snapshot") {
		t.Fatalf("design prompt still asks for a snapshot after apply: %q", prompt)
	}
}
