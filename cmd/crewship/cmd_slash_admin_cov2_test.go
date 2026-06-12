package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlashInitRunE_WriteFileError(t *testing.T) {
	dir := setupSlashHome(t)
	// Pre-create the commands dir read-only: MkdirAll succeeds (already
	// exists), the sample does not exist, but WriteFile is denied.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let TempDir cleanup work

	err := slashInitCmd.RunE(slashInitCmd, nil)
	if err == nil {
		t.Fatal("want write error on read-only commands dir; got nil")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("got %v; want permission denied", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "review.md")); statErr == nil {
		t.Error("sample must not exist after failed write")
	}
}
