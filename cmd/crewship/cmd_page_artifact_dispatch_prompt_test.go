package main

import (
	"strings"
	"testing"
)

// The CLI sends the viewer's exact instruction; like the viewer it must not
// ask for a design_snapshot after design_apply, which already saved a version.
func TestArtifactCommentPromptDesignDoesNotAskForSnapshot(t *testing.T) {
	prompt := artifactCommentPrompt("design", "page-1", artifactQueuedComment{ID: "comment-1", Version: 2, Selector: "frame-cart", Text: "Shorten the title"})
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
