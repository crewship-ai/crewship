package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func TestAssertInsideRoot_AcceptsOrdinaryPaths(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "daily"))

	// A file that does not exist yet is fine — the caller is about to
	// create it, and refusing would break every first write.
	if err := AssertInsideRoot(root, filepath.Join(root, "AGENT.md")); err != nil {
		t.Errorf("AssertInsideRoot on a new file: %v", err)
	}
	if err := AssertInsideRoot(root, filepath.Join(root, "daily", "2026-08-01.md")); err != nil {
		t.Errorf("AssertInsideRoot on a new nested file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "AGENT.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AssertInsideRoot(root, filepath.Join(root, "AGENT.md")); err != nil {
		t.Errorf("AssertInsideRoot on an existing regular file: %v", err)
	}
}

func TestAssertInsideRoot_RefusesLexicalEscape(t *testing.T) {
	root := t.TempDir()
	if err := AssertInsideRoot(root, filepath.Join(root, "..", "escape.md")); err == nil {
		t.Fatal("AssertInsideRoot accepted a ../ escape")
	}
}

// A parent that does not exist is fail-closed: the caller is expected to
// have created it (safely) first, so a missing one means something in
// the sequence went wrong.
func TestAssertInsideRoot_MissingParentFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := AssertInsideRoot(root, filepath.Join(root, "nope", "x.md")); err == nil {
		t.Fatal("AssertInsideRoot accepted a path under a non-existent parent")
	}
}

func TestEnsureDirNoFollow_CreatesNestedDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "peers")
	if err := EnsureDirNoFollow(root, dir); err != nil {
		t.Fatalf("EnsureDirNoFollow: %v", err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
	// Idempotent — a second call on an existing dir is not an error.
	if err := EnsureDirNoFollow(root, dir); err != nil {
		t.Errorf("second EnsureDirNoFollow: %v", err)
	}
}

func TestEnsureDirNoFollow_RefusesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDirNoFollow(root, filepath.Join(root, "..", "elsewhere")); err == nil {
		t.Fatal("EnsureDirNoFollow accepted a directory outside the root")
	}
}
