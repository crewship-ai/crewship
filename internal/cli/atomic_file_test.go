package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicFile_CommitWritesFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	a, err := NewAtomicFile(target)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := a.WriteString("hello world"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := a.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("file content: %q", string(got))
	}
}

func TestAtomicFile_CloseWithoutCommitDiscards(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "discarded.txt")

	a, _ := NewAtomicFile(target)
	_, _ = a.WriteString("partial write")
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target should not exist after close-without-commit")
	}
	// Tempfile cleaned up too — no .tmp-* leftover.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == filepath.Base(target) {
			continue // shouldn't be present, but just in case
		}
		t.Errorf("leftover file: %s", e.Name())
	}
}

func TestAtomicFile_CloseAfterCommitNoop(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	a, _ := NewAtomicFile(target)
	_, _ = a.WriteString("done")
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	// Close after commit must NOT delete the target.
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("close should not remove committed target: %v", err)
	}
}

func TestAtomicFile_PreservesExistingOnUncommittedClose(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatal(err)
	}

	a, _ := NewAtomicFile(target)
	_, _ = a.WriteString("REPLACEMENT")
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(target)
	if string(got) != "ORIGINAL" {
		t.Errorf("close-without-commit must not modify target: got %q", string(got))
	}
}

func TestAtomicFile_DoubleCommit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	a, _ := NewAtomicFile(target)
	_, _ = a.WriteString("once")
	if err := a.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(); err != nil {
		t.Errorf("second commit should be no-op, got: %v", err)
	}
}

func TestAtomicFile_NewInUnwritableDir(t *testing.T) {
	_, err := NewAtomicFile("/this/path/does/not/exist/file.txt")
	if err == nil {
		t.Error("expected error creating in non-existent directory")
	}
}
