//go:build !windows

package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMiseLockBundleRejectsLinkedV4Directory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "opencode", "package.json"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mise.lock"), []byte("lockfile_version = 4\naube = { path = \"locks/opencode\" }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "locks")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadMiseLockBundle(dir); err == nil {
		t.Fatalf("linked locks directory accepted: %+v", b.Files)
	}
}

func TestReadMiseLockBundleRejectsSymlinkedV4Reference(t *testing.T) {
	write := func(t *testing.T, dir string, files map[string]string) {
		t.Helper()
		for name, data := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ref := func(p string) string { return "lockfile_version = 4\naube = { path = \"" + p + "\" }\n" }
	dir := t.TempDir()
	outside := t.TempDir()
	write(t, outside, map[string]string{"package.json": "outside"})
	write(t, dir, map[string]string{"mise.lock": ref("locks/opencode/1")})
	if err := os.MkdirAll(filepath.Join(dir, "locks/opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "locks/opencode/1")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMiseLockBundle(dir); err == nil {
		t.Fatal("symlinked reference accepted")
	}
}
