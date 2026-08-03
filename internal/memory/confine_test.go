package memory

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
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

// The leaf itself being a symlink is the classic swap: os.WriteFile
// follows it and overwrites whatever it points at.
func TestAssertInsideRoot_RefusesSymlinkedLeaf(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim.md")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "AGENT.md")
	mustSymlink(t, outside, link)

	if err := AssertInsideRoot(root, link); err == nil {
		t.Fatal("AssertInsideRoot accepted a symlinked leaf")
	}
}

// The parent directory being a symlink is the one lexical confinement
// misses entirely: "daily/x.md" is textually inside the root while the
// bytes land wherever daily/ points.
func TestAssertInsideRoot_RefusesSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	mustSymlink(t, elsewhere, filepath.Join(root, "daily"))

	err := AssertInsideRoot(root, filepath.Join(root, "daily", "2026-08-01.md"))
	if err == nil {
		t.Fatal("AssertInsideRoot accepted a path whose parent is a symlink out of the root")
	}
	if !strings.Contains(err.Error(), "escape") {
		t.Errorf("error should name the escape; got: %v", err)
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

// Creating "daily/" when daily is already a symlink must fail rather
// than quietly succeed on the far side of the link.
func TestEnsureDirNoFollow_RefusesExistingSymlinkSegment(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	mustSymlink(t, elsewhere, filepath.Join(root, "daily"))

	err := EnsureDirNoFollow(root, filepath.Join(root, "daily"))
	if err == nil {
		t.Fatal("EnsureDirNoFollow accepted a symlinked segment")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error should name the symlink; got: %v", err)
	}
}

func TestEnsureDirNoFollow_RefusesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDirNoFollow(root, filepath.Join(root, "..", "elsewhere")); err == nil {
		t.Fatal("EnsureDirNoFollow accepted a directory outside the root")
	}
}

func TestReadFileNoFollow_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(target, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "notes.md")
	mustSymlink(t, target, link)

	if _, err := ReadFileNoFollow(link); err == nil {
		t.Fatal("ReadFileNoFollow followed a symlink")
	}

	plain := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(plain, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFileNoFollow(plain)
	if err != nil {
		t.Fatalf("ReadFileNoFollow on a regular file: %v", err)
	}
	if string(got) != "body" {
		t.Errorf("content = %q, want %q", got, "body")
	}
}
