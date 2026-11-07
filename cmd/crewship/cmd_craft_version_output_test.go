//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// `craft version --output` writes private document bytes. An existing file
// must not keep its old mode, and a symlink must not redirect the bytes.
func TestCraftWritePrivateFileIsAlways0600(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("old public content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := craftWritePrivateFile(existing, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("overwritten file mode %o, want 600", mode)
	}
	if got, _ := os.ReadFile(existing); string(got) != `{"v":1}` {
		t.Fatalf("content %q", got)
	}

	fresh := filepath.Join(dir, "fresh.json")
	if err := craftWritePrivateFile(fresh, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("new file: %v %v", info, err)
	}

	// A symlink at the destination is replaced by the private file; the file
	// it pointed to is left untouched and keeps its mode.
	elsewhere := filepath.Join(t.TempDir(), "shared.json")
	if err := os.WriteFile(elsewhere, []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if err := craftWritePrivateFile(link, []byte(`{"v":3}`)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("symlink destination: %v %v", info, err)
	}
	if got, _ := os.ReadFile(elsewhere); string(got) != "shared" {
		t.Fatalf("symlink target was followed: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
