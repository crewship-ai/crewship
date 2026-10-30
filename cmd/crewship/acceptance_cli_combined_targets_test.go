//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The artifact target metadata and attachment extras were added separately.
// Their combined send must keep the target's exact key and closed-object shape.
func TestAcceptance_CombinedArtifactTargetAndAttachment(t *testing.T) {
	s := newParityStub(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(target, []byte(`{"submission_id":"combined-target"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("Own QA attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	s.onFrame = replyOnce
	_, errOut, err := s.run("run", "viktor", "--chat", parityChatID, "--artifact-target", target, "--attach", file, "Describe it: a QA page")
	if err != nil {
		t.Fatalf("combined send: %v %s", err, errOut)
	}
	sends := s.Frames("send_message")
	if len(sends) != 1 {
		t.Fatalf("send count=%d", len(sends))
	}
	got := decodeSent(t, sends[0])
	intent, ok := got.Metadata["artifact_project_intent"].(map[string]any)
	if !ok || len(intent) != 1 || intent["submission_id"] != "combined-target" {
		t.Fatalf("artifact target lost or reshaped: %#v", got.Metadata)
	}
	if _, ok := got.Metadata["document_iteration_intent"]; ok {
		t.Fatal("artifact target relabeled as Document iteration")
	}
	if !strings.Contains(got.Content, "Describe it: a QA page") || !strings.Contains(got.Content, "attachments/"+parityChatID+"/notes.txt") {
		t.Fatalf("attachment content lost: %s", got.Content)
	}
	if len(s.Requests("POST", "/api/v1/agents/"+parityAgentID+"/chats/"+parityChatID+"/attachments")) != 1 {
		t.Fatal("attachment not uploaded once")
	}
}
