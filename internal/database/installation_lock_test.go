package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallationOperationRefusesEscapedRunAndLockSymlinks(t *testing.T) {
	for _, kind := range []string{"run", "anchor"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if kind == "run" {
				if err := os.Symlink(outside, filepath.Join(root, "run")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(root, "run"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "anchor"), filepath.Join(root, "run", "installation.lock")); err != nil {
					t.Fatal(err)
				}
			}
			if lease, err := AcquireInstallationOperation(root); err == nil {
				lease.Close()
				t.Fatal("accepted escaping coordination path")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatal("created outside installation", entries, err)
			}
		})
	}
}
