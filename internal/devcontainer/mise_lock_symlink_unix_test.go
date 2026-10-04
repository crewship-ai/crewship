//go:build !windows

package devcontainer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadMiseLockBundleRejectsLinks(t *testing.T) {
	for _, kind := range []string{"symlink lock", "symlink auxiliary", "symlink parent"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "mise.lock"), []byte("version=3"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink lock":
				if err := os.Remove(filepath.Join(dir, "mise.lock")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "mise.lock")); err != nil {
					t.Fatal(err)
				}
			case "symlink auxiliary":
				if err := os.MkdirAll(filepath.Join(dir, ".mise/locks/a"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, ".mise/locks/a/link")); err != nil {
					t.Fatal(err)
				}
			case "symlink parent":
				if err := os.Symlink(outside, filepath.Join(dir, ".mise")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReadMiseLockBundle(dir); err == nil {
				t.Fatal("unsafe bundle accepted")
			}
		})
	}
}
