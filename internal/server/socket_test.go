package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveSocketFileNonexistent(t *testing.T) {
	dir := t.TempDir()
	err := removeSocketFile(filepath.Join(dir, "nonexistent.sock"))
	if err != nil {
		t.Fatalf("expected no error for nonexistent file, got: %v", err)
	}
}

func TestRemoveSocketFileExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.sock")

	if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	err := removeSocketFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed")
	}
}

func TestRemoveSocketFileCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "test.sock")

	err := removeSocketFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parentDir := filepath.Dir(path)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("expected parent dir to exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected parent to be a directory")
	}
}
