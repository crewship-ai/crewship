package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRootedEntryPointsRequireIndependentRootBeforeAnyIO(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "must-not-exist")
	path := filepath.Join(parent, "AGENT.md")
	if _, err := MutateRooted(context.Background(), nil, MutateRequest{Path: path}); err == nil {
		t.Fatal("empty mutation root accepted")
	}
	if _, err := ReadCanonicalRooted(context.Background(), nil, ReadRequest{Path: path}); err == nil {
		t.Fatal("empty read root accepted")
	}
	if _, err := RestoreRooted(context.Background(), nil, path, "ws", "AGENT.md", "sha", "operator", "blobs", TierAgent, ""); err == nil {
		t.Fatal("empty restore root accepted before ledger access")
	}
	if _, err := os.Lstat(parent); !os.IsNotExist(err) {
		t.Fatalf("rootless entrypoint touched path: %v", err)
	}
}

func TestRootedFileDoesNotCarryLegacyPathCapability(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENT.md")
	f, err := openRootedMutationFile(root, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.close()
	if f.path != "" {
		t.Fatal("rooted capability retained a legacy pathname")
	}
	if err := f.write([]byte("inside")); err != nil {
		t.Fatal(err)
	}
	got, err := f.read()
	if err != nil || string(got) != "inside" {
		t.Fatalf("rooted content=%q %v", got, err)
	}
}
