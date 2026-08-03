//go:build !windows

package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Symlink behaviour is the half of confinement that lexical checks
// cannot cover, so it needs real links. Creating one needs privileges
// on Windows; rather than a runtime t.Skip — which reports the same
// "ok" as a real pass — the whole file is a compile-time decision.

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
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
