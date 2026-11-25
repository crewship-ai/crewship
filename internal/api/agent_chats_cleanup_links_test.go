package api

import (
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func cleanupHandler(base string) *AgentHandler {
	return &AgentHandler{storagePath: base, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func runCleanup(h *AgentHandler) {
	h.cleanupChatAttachments(sql.NullString{String: "crew1", Valid: true}, sql.NullString{String: "slug1", Valid: true}, "chat1")
}

// Deleting a chat removes its attachment directory, and nothing else.
func TestCleanupChatAttachmentsRemovesTheChatDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "crew1", "slug1", "attachments", "chat1", "att1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "crew1", "slug1", "attachments", "chat2")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	runCleanup(cleanupHandler(base))
	if _, err := os.Stat(filepath.Join(base, "crew1", "slug1", "attachments", "chat1")); !os.IsNotExist(err) {
		t.Fatalf("chat directory still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another chat's directory was removed: %v", err)
	}
}

// A chat with no attachment directory, or an agent with no crew directory,
// is not an error.
func TestCleanupChatAttachmentsToleratesMissingDirectories(t *testing.T) {
	base := t.TempDir()
	runCleanup(cleanupHandler(base))
	if err := os.MkdirAll(filepath.Join(base, "crew1"), 0o700); err != nil {
		t.Fatal(err)
	}
	runCleanup(cleanupHandler(base))
}

// The agent can write the crew's output directory and replace a directory in
// it with a link: cleanup must never remove anything through it.
func TestCleanupChatAttachmentsDoesNotFollowAgentLinks(t *testing.T) {
	for _, name := range []string{"slug is a link", "attachments is a link", "chat directory is a link"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			victim := filepath.Join(outside, "attachments", "chat1")
			if err := os.MkdirAll(victim, 0o700); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(victim, "keep.txt")
			if err := os.WriteFile(keep, []byte("not the agent's"), 0o600); err != nil {
				t.Fatal(err)
			}
			crew := filepath.Join(base, "crew1")
			if err := os.MkdirAll(crew, 0o700); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "slug is a link":
				if err := os.Symlink(outside, filepath.Join(crew, "slug1")); err != nil {
					t.Fatal(err)
				}
			case "attachments is a link":
				if err := os.MkdirAll(filepath.Join(crew, "slug1"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "attachments"), filepath.Join(crew, "slug1", "attachments")); err != nil {
					t.Fatal(err)
				}
			case "chat directory is a link":
				if err := os.MkdirAll(filepath.Join(crew, "slug1", "attachments"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Join(crew, "slug1", "attachments", "chat1")); err != nil {
					t.Fatal(err)
				}
			}
			runCleanup(cleanupHandler(base))
			if _, err := os.Stat(keep); err != nil {
				t.Fatalf("cleanup removed a file outside the crew directory: %v", err)
			}
			if name == "chat directory is a link" {
				if _, err := os.Lstat(filepath.Join(crew, "slug1", "attachments", "chat1")); !os.IsNotExist(err) {
					t.Errorf("the link itself was not removed: %v", err)
				}
			}
		})
	}
}
